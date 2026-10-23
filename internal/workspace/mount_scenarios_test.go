package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"vault/internal/fusefs/fusetest"
	"vault/internal/model"
	"vault/internal/procfilter"
	"vault/internal/trash"
)

func mountHome(t *testing.T) (*testHome, string) {
	t.Helper()
	home := newTestHome(t)
	root, mount := fusetest.Mount(t, home.ws)
	home.ws.AttachInvalidator(mount)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { home.ws.RunInvalidations(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return home, root
}

func shell(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("sh", "-ec", script)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, output)
	}
}

func oidAt(t *testing.T, home *testHome, path string) model.ObjectID {
	t.Helper()
	entry, ok := home.ws.tree.Resolve(path)
	if !ok {
		t.Fatalf("%s missing from tree", path)
	}
	return entry.ID
}

func TestGeditStyleSaveKeepsObjectAcrossSaves(t *testing.T) {
	home, root := mountHome(t)
	shell(t, root, "printf 'first' > doc.txt")
	original := oidAt(t, home, "/doc.txt")
	holder, err := os.Open(filepath.Join(root, "doc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	for i, text := range []string{"second", "third"} {
		shell(t, root, "printf '"+text+"' > .goutputstream-X"+string(rune('A'+i))+" && sync && mv .goutputstream-X"+string(rune('A'+i))+" doc.txt")
		if id := oidAt(t, home, "/doc.txt"); id != original {
			t.Fatalf("save %d changed doc.txt oid to %s", i+1, id)
		}
		data, _ := os.ReadFile(filepath.Join(root, "doc.txt"))
		if string(data) != text {
			t.Fatalf("save %d reads %q", i+1, data)
		}
	}
	shell(t, root, "printf ' appended' >> doc.txt")
	if id := oidAt(t, home, "/doc.txt"); id != original {
		t.Fatal("append after saves hit another object")
	}
	old := make([]byte, 16)
	n, _ := holder.ReadAt(old, 0)
	if string(old[:n]) != "first" {
		t.Fatalf("reader holding the original sees %q", old[:n])
	}
}

func TestVimStyleSaveLeavesDeleteAndCreateForTheReducer(t *testing.T) {
	home, root := mountHome(t)
	shell(t, root, "printf 'v1' > notes.md")
	home.clearPendingOps(t)
	shell(t, root, "printf x > .notes.md.swp && mv notes.md notes.md~ && printf 'v2' > notes.md && rm notes.md~ && rm .notes.md.swp")
	kinds := opKinds(home.pendingOps(t))
	if !slices.Contains(kinds, "delete:/notes.md~") || !slices.Contains(kinds, "create:/notes.md") {
		t.Fatalf("vim save ops = %v", kinds)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "notes.md")); string(data) != "v2" {
		t.Fatalf("notes.md = %q", data)
	}
}

func TestCopyTreeInAndChmod(t *testing.T) {
	home, root := mountHome(t)
	source := t.TempDir()
	shell(t, source, "mkdir -p Photos/2024 && for i in 1 2 3 4 5; do head -c 70000 /dev/urandom > Photos/2024/p$i.jpg; done && printf '#!/bin/sh\\necho hi\\n' > Photos/run.sh")
	shell(t, root, "cp -r "+source+"/Photos . && chmod +x Photos/run.sh && ./Photos/run.sh > /dev/null")
	if out, err := exec.Command("diff", "-r", filepath.Join(source, "Photos"), filepath.Join(root, "Photos")).CombinedOutput(); err != nil {
		t.Fatalf("copied tree differs: %s", out)
	}
	if entry, _ := home.ws.tree.Resolve("/Photos/run.sh"); !entry.Executable {
		t.Fatal("chmod +x not recorded")
	}
}

func TestMoveOutAcrossFilesystemsIsADelete(t *testing.T) {
	home, root := mountHome(t)
	shell(t, root, "mkdir Clips && printf 'video' > Clips/clip.mp4")
	outside := t.TempDir()
	shell(t, root, "mv Clips "+outside+"/")
	if data, err := os.ReadFile(filepath.Join(outside, "Clips", "clip.mp4")); err != nil || string(data) != "video" {
		t.Fatalf("moved file = %q, %v", data, err)
	}
	kinds := opKinds(home.pendingOps(t))
	if !slices.Contains(kinds, "delete:/Clips/clip.mp4") || !slices.Contains(kinds, "delete:/Clips") {
		t.Fatalf("ops = %v", kinds)
	}
}

func TestDesktopTrashThroughMount(t *testing.T) {
	home, root := mountHome(t)
	shell(t, root, "printf 'q3' > report.pdf && mkdir -p .Trash-1000/files .Trash-1000/info && printf '[Trash Info]\\nPath=/report.pdf\\n' > .Trash-1000/info/report.pdf.trashinfo && mv report.pdf .Trash-1000/files/")
	for _, name := range []string{"files/report.pdf", "info/report.pdf.trashinfo"} {
		if _, ok := home.ws.tree.Resolve("/.Trash-1000/" + name); ok {
			t.Errorf("%s still in the tree", name)
		}
	}
	for _, op := range home.pendingOps(t) {
		if strings.Contains(op.Path, ".Trash") {
			t.Fatalf("desktop trash queued an op: %+v", op)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "report.pdf")); !os.IsNotExist(err) {
		t.Fatalf("report.pdf still visible: %v", err)
	}
}

func TestRefusedOperationsThroughMount(t *testing.T) {
	_, root := mountHome(t)
	shell(t, root, "printf x > a")
	if err := os.Symlink("a", filepath.Join(root, "link")); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := syscall.Rmdir(root); err == nil {
		t.Fatal("removed mount root")
	}
}

func TestMoveOutIsRecognisedForFilesAndFolders(t *testing.T) {
	home := newTestHome(t)
	detector := trash.NewMoveOutDetector(procfilter.SystemProc(), 10*time.Second, time.Now)
	home.ws.Reads, home.ws.Classify = detector, detector
	root, mount := fusetest.Mount(t, home.ws)
	home.ws.AttachInvalidator(mount)
	shell(t, root, "printf 'clip' > clip.mp4 && mkdir Album && printf 1 > Album/a.jpg && printf 2 > Album/b.jpg && printf x > junk.tmp")
	outside := t.TempDir()
	shell(t, root, "mv clip.mp4 Album "+outside+"/ && rm junk.tmp")
	reasons := map[string]model.DeleteReason{}
	for _, op := range home.pendingOps(t) {
		if op.Kind == model.LocalDelete {
			reasons[op.Path] = op.Reason
		}
	}
	for _, path := range []string{"/clip.mp4", "/Album/a.jpg", "/Album/b.jpg"} {
		if reasons[path] != model.ReasonMovedOut {
			t.Errorf("%s labelled %q", path, reasons[path])
		}
	}
	if reasons["/junk.tmp"] != model.ReasonDeleted {
		t.Errorf("rm labelled %q", reasons["/junk.tmp"])
	}
}
