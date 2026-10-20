// Package fusetest mounts a fusefs.Store on a temporary directory for integration tests.
package fusetest

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/fusefs"
)

// Mount serves store at a fresh temporary directory and unmounts it when the test ends.
// It skips the test when FUSE is unavailable, for example inside a container.
func Mount(t *testing.T, store fusefs.Store) (string, *fusefs.Mount) {
	t.Helper()
	skipWithoutFUSE(t)
	mountPoint := t.TempDir()
	mount, err := fusefs.MountStore(store, fusefs.Options{
		MountPoint: mountPoint, UID: uint32(os.Getuid()), GID: uint32(os.Getgid()),
		EntryTimeout: time.Second, AttrTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("FUSE mount not permitted: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unmount(t, mount) })
	return mountPoint, mount
}

func skipWithoutFUSE(t *testing.T) {
	t.Helper()
	if os.Getenv("VAULT_SKIP_FUSE") == "1" {
		t.Skip("VAULT_SKIP_FUSE=1")
	}
	device, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("/dev/fuse unavailable: %v", err)
	}
	device.Close()
	if _, err := exec.LookPath("fusermount3"); err != nil {
		if _, err := exec.LookPath("fusermount"); err != nil {
			t.Skip("fusermount not installed")
		}
	}
}

func unmount(t *testing.T, mount *fusefs.Mount) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := mount.Unmount()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			if err := mount.UnmountLazy(); err != nil {
				t.Errorf("unmount: %v", err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
