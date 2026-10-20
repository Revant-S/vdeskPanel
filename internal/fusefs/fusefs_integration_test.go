package fusefs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/fusefs/fusetest"
	"github.com/cosmoboy/vault/internal/model"
	"golang.org/x/sys/unix"
)

func mountMemory(t *testing.T) (string, *fusefs.MemoryStore, *fusefs.Mount) {
	t.Helper()
	store := fusefs.NewMemoryStore()
	root, mount := fusetest.Mount(t, store)
	return root, store, mount
}

func TestFilesAndFoldersBehaveLikeAFolder(t *testing.T) {
	root, _, _ := mountMemory(t)
	dir := filepath.Join(root, "Notes")
	must(t, os.Mkdir(dir, 0o755))
	path := filepath.Join(dir, "gate.md")
	must(t, os.WriteFile(path, []byte("hello vault"), 0o644))
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello vault" {
		t.Fatalf("read back %q, %v", data, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	file.WriteString("!")
	must(t, file.Sync())
	file.Close()
	must(t, os.Truncate(path, 5))
	if data, _ := os.ReadFile(path); string(data) != "hello" {
		t.Fatalf("after truncate %q", data)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "Notes" || !entries[0].IsDir() {
		t.Fatalf("readdir = %v, %v", entries, err)
	}
	must(t, os.Rename(path, filepath.Join(root, "moved.md")))
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old path still there: %v", err)
	}
	if err := os.Remove(root); err == nil {
		t.Fatal("removed the mount root")
	}
	must(t, os.WriteFile(filepath.Join(dir, "x"), nil, 0o644))
	if err := syscall.Rmdir(dir); err != syscall.ENOTEMPTY {
		t.Fatalf("rmdir non-empty = %v", err)
	}
	must(t, os.Remove(filepath.Join(dir, "x")))
	must(t, os.Remove(dir))
}

func TestRenameOverAndRenameFlags(t *testing.T) {
	root, _, _ := mountMemory(t)
	target, temp := filepath.Join(root, "doc.txt"), filepath.Join(root, ".goutputstream-AB12")
	must(t, os.WriteFile(target, []byte("old"), 0o644))
	must(t, os.WriteFile(temp, []byte("new"), 0o644))
	if err := unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); err != unix.EEXIST {
		t.Fatalf("RENAME_NOREPLACE onto existing = %v", err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, target, unix.RENAME_EXCHANGE); err != unix.EINVAL {
		t.Fatalf("RENAME_EXCHANGE = %v", err)
	}
	must(t, os.Rename(temp, target))
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatalf("after rename over: %q", data)
	}
}

func TestModeAndTimes(t *testing.T) {
	root, _, _ := mountMemory(t)
	path := filepath.Join(root, "run.sh")
	must(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644))
	must(t, os.Chmod(path, 0o700))
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("chmod +x mode = %v", info.Mode())
	}
	must(t, os.Chmod(path, 0o600))
	info, _ = os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("chmod -x mode = %v", info.Mode())
	}
	stamp := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	must(t, os.Chtimes(path, stamp, stamp))
	info, _ = os.Stat(path)
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("mtime = %v", info.ModTime())
	}
}

func TestUnsupportedObjectsAreRefused(t *testing.T) {
	root, _, _ := mountMemory(t)
	path := filepath.Join(root, "a")
	must(t, os.WriteFile(path, nil, 0o644))
	if err := os.Symlink("a", filepath.Join(root, "link")); !errors.Is(err, syscall.EPERM) {
		t.Errorf("symlink = %v", err)
	}
	if err := os.Link(path, filepath.Join(root, "hard")); !errors.Is(err, syscall.EPERM) {
		t.Errorf("link = %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o644); !errors.Is(err, syscall.EPERM) {
		t.Errorf("mkfifo = %v", err)
	}
	if err := unix.Setxattr(path, "user.tag", []byte("x"), 0); !errors.Is(err, syscall.ENOTSUP) {
		t.Errorf("setxattr = %v", err)
	}
	if _, err := unix.Getxattr(path, "user.tag", make([]byte, 8)); !errors.Is(err, syscall.ENODATA) {
		t.Errorf("getxattr = %v", err)
	}
}

func TestStatfsAndExternalTools(t *testing.T) {
	root, store, _ := mountMemory(t)
	var stats unix.Statfs_t
	must(t, unix.Statfs(root, &stats))
	if stats.Bsize != 4096 || stats.Blocks != (1<<30)/4096 {
		t.Fatalf("statfs = %+v", stats)
	}
	outside := filepath.Join(t.TempDir(), "source.bin")
	must(t, os.WriteFile(outside, []byte(strings.Repeat("x", 300_000)), 0o644))
	run(t, "cp", outside, filepath.Join(root, "copied.bin"))
	run(t, "mv", filepath.Join(root, "copied.bin"), filepath.Join(root, "renamed.bin"))
	if data, err := os.ReadFile(filepath.Join(root, "renamed.bin")); err != nil || len(data) != 300_000 {
		t.Fatalf("copied file: %d bytes, %v", len(data), err)
	}
	pid := strings.TrimSpace(run(t, "sh", "-c", "echo x > "+filepath.Join(root, "by-shell")+"; echo $$"))
	caller := store.LastCaller()
	if strconv.Itoa(int(caller.PID)) != pid {
		t.Fatalf("caller pid = %d, shell pid = %s", caller.PID, pid)
	}
}

func TestInvalidateContentDropsKernelCache(t *testing.T) {
	root, store, mount := mountMemory(t)
	path := filepath.Join(root, "cached.txt")
	must(t, os.WriteFile(path, []byte("version one"), 0o644))
	if data, _ := os.ReadFile(path); string(data) != "version one" {
		t.Fatal("first read wrong")
	}
	info, _ := os.Stat(path)
	ino := info.Sys().(*syscall.Stat_t).Ino
	store.ReplaceContent(model.Ino(ino), []byte("version two"))
	mount.InvalidateContent(model.Ino(ino))
	if data, _ := os.ReadFile(path); string(data) != "version two" {
		t.Fatalf("after invalidation read %q", data)
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
