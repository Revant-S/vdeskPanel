package workspace

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/plaintextcheck"
)

func TestCreateWriteAndReopenAfterRestart(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Notes")
	id := home.writeFile(t, "/Notes", "gate.md", "graph theory notes")
	if got := home.readFile(t, "/Notes/gate.md"); got != "graph theory notes" {
		t.Fatalf("read %q", got)
	}
	object := home.object(t, id)
	if object.State != model.Dirty || object.Size != 18 || object.ContentVer.IsZero() {
		t.Fatalf("object = %+v", object)
	}
	want := []string{"create:/Notes", "create:/Notes/gate.md", "modify:/Notes/gate.md"}
	if got := opKinds(home.pendingOps(t)); !slices.Equal(got, want) {
		t.Fatalf("ops = %v, want %v", got, want)
	}
	home.restart(t)
	if got := home.readFile(t, "/Notes/gate.md"); got != "graph theory notes" {
		t.Fatalf("after restart read %q", got)
	}
}

func TestMoveKeepsEncryptedNameAndRenameChangesIt(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Papers")
	id := home.writeFile(t, "/", "a.pdf", "pdf")
	before := home.object(t, id).EncName
	ctx := context.Background()
	if err := home.ws.Rename(ctx, rootIno, "a.pdf", home.inodeOfPath(t, "/Papers"), "a.pdf", false); err != nil {
		t.Fatal(err)
	}
	if home.object(t, id).EncName != before {
		t.Fatal("move re-encrypted the name")
	}
	if err := home.ws.Rename(ctx, home.inodeOfPath(t, "/Papers"), "a.pdf", home.inodeOfPath(t, "/Papers"), "b.pdf", false); err != nil {
		t.Fatal(err)
	}
	if home.object(t, id).EncName == before {
		t.Fatal("rename kept the old encrypted name")
	}
	ops := home.pendingOps(t)
	last := ops[len(ops)-1]
	if last.Kind != model.LocalMove || last.FromPath != "/Papers/a.pdf" || last.Path != "/Papers/b.pdf" {
		t.Fatalf("last op = %+v", last)
	}
	home.restart(t)
	if _, ok := home.ws.tree.Resolve("/Papers/b.pdf"); !ok {
		t.Fatal("renamed file missing after restart")
	}
}

func TestRenameOverKeepsTargetIdentity(t *testing.T) {
	home := newTestHome(t)
	target := home.writeFile(t, "/", "doc.txt", "old text")
	ctx := context.Background()
	reader, _, err := home.ws.Open(ctx, home.inodeOfPath(t, "/doc.txt"), false, false, fusefs.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release(ctx)
	source := home.writeFile(t, "/", ".goutputstream-AB12", "new text")
	sourceIno := home.inodeOfPath(t, "/.goutputstream-AB12")
	if err := home.ws.Rename(ctx, rootIno, ".goutputstream-AB12", rootIno, "doc.txt", false); err != nil {
		t.Fatal(err)
	}
	entry, _ := home.ws.tree.Resolve("/doc.txt")
	if entry.ID != target {
		t.Fatalf("doc.txt has oid %s, want target %s", entry.ID, target)
	}
	if id, _ := home.ws.inodes.objectOf(sourceIno); id != target {
		t.Fatalf("source inode maps to %s, want target", id)
	}
	if got := home.readFile(t, "/doc.txt"); got != "new text" {
		t.Fatalf("doc.txt reads %q", got)
	}
	old := make([]byte, 8)
	if n, _ := reader.ReadAt(ctx, old, 0); string(old[:n]) != "old text" {
		t.Fatalf("reader holding the old file sees %q", old[:n])
	}
	ops := home.pendingOps(t)
	modify, absorb := ops[len(ops)-2], ops[len(ops)-1]
	if modify.Kind != model.LocalModify || modify.ObjectID != target || modify.ReplacedFrom != source || absorb.Kind != model.LocalAbsorb || absorb.ObjectID != source {
		t.Fatalf("rename-over ops = %+v, %+v", modify, absorb)
	}
	home.restart(t)
	if got := home.readFile(t, "/doc.txt"); got != "new text" {
		t.Fatalf("after restart doc.txt reads %q", got)
	}
	if _, ok := home.ws.tree.Resolve("/.goutputstream-AB12"); ok {
		t.Fatal("absorbed temp file came back after restart")
	}
}

func TestDeleteHidesObjectButKeepsRowUntilSynced(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Photos")
	id := home.writeFile(t, "/Photos", "cat.jpg", "meow")
	ctx := context.Background()
	if err := home.ws.Rmdir(ctx, rootIno, "Photos", fusefs.Caller{}); err != syscall.ENOTEMPTY {
		t.Fatalf("rmdir non-empty = %v", err)
	}
	if err := home.ws.Unlink(ctx, home.inodeOfPath(t, "/Photos"), "cat.jpg", fusefs.Caller{PID: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := home.ws.Lookup(ctx, home.inodeOfPath(t, "/Photos"), "cat.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lookup after unlink = %v", err)
	}
	if object := home.object(t, id); object.DeleteVer.IsZero() {
		t.Fatal("deleted row has no tombstone")
	}
	ops := home.pendingOps(t)
	if last := ops[len(ops)-1]; last.Kind != model.LocalDelete || last.Path != "/Photos/cat.jpg" || last.Reason != model.ReasonDeleted {
		t.Fatalf("delete op = %+v", last)
	}
	home.restart(t)
	if _, ok := home.ws.tree.Resolve("/Photos/cat.jpg"); ok {
		t.Fatal("deleted file came back after restart")
	}
}

func TestDesktopTrashIsLocalOnlyAndDeletesFromVault(t *testing.T) {
	home := newTestHome(t)
	id := home.writeFile(t, "/", "report.pdf", "quarterly")
	home.mkdir(t, "/", ".Trash-1000")
	home.mkdir(t, "/.Trash-1000", "files")
	home.mkdir(t, "/.Trash-1000", "info")
	home.writeFile(t, "/.Trash-1000/info", "report.pdf.trashinfo", "[Trash Info]\nPath=/report.pdf\n")
	before := len(home.pendingOps(t))
	ctx := context.Background()
	if err := home.ws.Rename(ctx, rootIno, "report.pdf", home.inodeOfPath(t, "/.Trash-1000/files"), "report.pdf", false); err != nil {
		t.Fatal(err)
	}
	ops := home.pendingOps(t)
	if len(ops) != before+1 || ops[len(ops)-1].Kind != model.LocalDelete || ops[len(ops)-1].ObjectID != id {
		t.Fatalf("ops = %v", opKinds(ops))
	}
	for _, path := range []string{"/.Trash-1000/files/report.pdf", "/.Trash-1000/info/report.pdf.trashinfo"} {
		if _, ok := home.ws.tree.Resolve(path); ok {
			t.Errorf("%s still exists", path)
		}
	}
	for _, op := range ops {
		if strings.Contains(op.Path, ".Trash-1000") {
			t.Fatalf("desktop trash reached the op queue: %+v", op)
		}
	}
	plaintextcheck.AssertDirClean(t, home.dir, "Trash Info", "trashinfo", "quarterly")
}

func TestInterruptedWriteIsRequeuedOnRestart(t *testing.T) {
	home := newTestHome(t)
	ctx := context.Background()
	_, handle, err := home.ws.Create(ctx, rootIno, "draft.txt", false, fusefs.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	handle.WriteAt(ctx, []byte("half written"), 0)
	home.restart(t)
	if entry, _ := home.ws.tree.Resolve("/draft.txt"); entry.Size != 12 {
		t.Fatalf("size after crash = %d, want 12", entry.Size)
	}
	if got := opKinds(home.pendingOps(t)); !slices.Equal(got, []string{"create:/draft.txt"}) {
		t.Fatalf("ops after crash = %v", got)
	}

	home.clearPendingOps(t)
	handle, _, err = home.ws.Open(ctx, home.inodeOfPath(t, "/draft.txt"), true, false, fusefs.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	handle.WriteAt(ctx, []byte("fully written"), 0)
	home.restart(t)
	if got := opKinds(home.pendingOps(t)); !slices.Equal(got, []string{"modify:/draft.txt"}) {
		t.Fatalf("ops after crash of a synced file = %v", got)
	}
}

func TestChmodKeepsOnlyExecutableBitAndQueuesAttr(t *testing.T) {
	home := newTestHome(t)
	home.writeFile(t, "/", "run.sh", "#!/bin/sh")
	executable := true
	attr, err := home.ws.Setattr(context.Background(), home.inodeOfPath(t, "/run.sh"), fusefs.AttrChange{Executable: &executable})
	if err != nil || !attr.Executable {
		t.Fatalf("Setattr = %+v, %v", attr, err)
	}
	ops := home.pendingOps(t)
	if ops[len(ops)-1].Kind != model.LocalAttr {
		t.Fatalf("ops = %v", opKinds(ops))
	}
}

func TestNamesAndFreezeErrors(t *testing.T) {
	home := newTestHome(t)
	ctx := context.Background()
	if _, err := home.ws.Mkdir(ctx, rootIno, strings.Repeat("n", 256)); err != syscall.ENAMETOOLONG {
		t.Fatalf("long name = %v", err)
	}
	home.mkdir(t, "/", "dup")
	if _, err := home.ws.Mkdir(ctx, rootIno, "dup"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate mkdir = %v", err)
	}
	home.ws.Freeze()
	if _, err := home.ws.Lookup(ctx, rootIno, "dup"); err != syscall.EIO {
		t.Fatalf("lookup after freeze = %v", err)
	}
}

func TestIndexAndCacheHoldNoPlaintext(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Secret Plans")
	home.writeFile(t, "/Secret Plans", "launch-codes.txt", "the eagle lands at noon")
	plaintextcheck.AssertDirClean(t, filepath.Clean(home.dir), "Secret Plans", "launch-codes", "eagle lands")
}
