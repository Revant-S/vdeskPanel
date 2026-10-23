package workspace

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"vault/internal/fusefs"
	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
	"vault/internal/vaultfile"
)

// applyThenCrash commits a remote commit's index changes and stops before the disk effects
// that follow, as a process killed at that moment would.
func (h *testHome) applyThenCrash(t *testing.T, commit journal.Commit) {
	t.Helper()
	ctx := context.Background()
	apply := &remoteApply{ws: h.ws, ctx: ctx, commit: commit, unmoved: map[model.ObjectID]bool{}}
	err := h.ws.update(ctx, func(tx *index.Tx) error {
		apply.tx = tx
		for _, op := range commit.Ops {
			if err := apply.op(op); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.restart(t)
}

func (h *testHome) markSynced(t *testing.T, id model.ObjectID, driveID string) {
	t.Helper()
	err := h.index.Update(context.Background(), func(tx *index.Tx) error {
		object, err := tx.Object(id)
		if err != nil {
			return err
		}
		object.DriveID, object.State = driveID, model.Local
		return tx.PutObject(object)
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clearPendingOps(t)
}

func (h *testHome) overwrite(t *testing.T, path, content string) {
	t.Helper()
	ctx := context.Background()
	handle, _, err := h.ws.Open(ctx, h.inodeOfPath(t, path), true, true, fusefs.Caller{PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteAt(ctx, []byte(content), 0); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func (h *testHome) supersedeHolder(t *testing.T) index.Object {
	t.Helper()
	for _, op := range h.pendingOps(t) {
		if op.Kind == model.LocalSupersede {
			return h.object(t, op.ObjectID)
		}
	}
	t.Fatal("no supersede op queued")
	return index.Object{}
}

func (h *testHome) bodyOf(t *testing.T, object index.Object) string {
	t.Helper()
	ctx := context.Background()
	entry, err := h.cache.Open(ctx, object.ID, object.Size, object.State, vaultfile.BodyID{})
	if err != nil {
		t.Fatalf("open body of %s: %v", object.ID, err)
	}
	defer entry.Close()
	buf := make([]byte, 1<<16)
	n, err := entry.ReadAt(ctx, buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestCrashBeforeSupersededBodyMovesKeepsTheLocalEdit(t *testing.T) {
	home := newTestHome(t)
	id := home.writeFile(t, "/", "doc.txt", "base")
	home.markSynced(t, id, "drive-v1")
	home.overwrite(t, "/doc.txt", "local edit that loses")

	newer := model.Stamp{WallMillis: time.Now().Add(time.Minute).UnixMilli(), Device: "d-other"}
	home.applyThenCrash(t, journal.Commit{ID: "c-1", Device: "d-other", DeviceName: "other", Clock: newer, Ops: []journal.Op{{
		Kind: journal.OpModify, ObjectID: id, DriveID: "drive-v2", PreviousDriveID: "drive-v1", BodyID: make([]byte, 16),
		BodyMD5: "md5", Size: 12, ModTime: time.Now(), Version: newer}}})

	holder := home.supersedeHolder(t)
	if got := home.bodyOf(t, holder); got != "local edit that loses" {
		t.Fatalf("after the crash the superseded edit reads %q", got)
	}
	if home.cache.HasBody(id) {
		t.Fatal("the superseded body is still under the replaced file, where its next open overwrites it")
	}
	home.expectNoBodyMoves(t)
}

func (h *testHome) expectNoBodyMoves(t *testing.T) {
	t.Helper()
	err := h.index.View(context.Background(), func(tx *index.Tx) error {
		moves, err := tx.KVWithPrefix(bodyMoveKeyPrefix)
		if len(moves) != 0 {
			t.Fatalf("body moves left recorded: %v", moves)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSupersededBodyMoveIsNotLeftRecorded(t *testing.T) {
	home := newTestHome(t)
	id := home.writeFile(t, "/", "doc.txt", "base")
	home.markSynced(t, id, "drive-v1")
	home.overwrite(t, "/doc.txt", "local edit that loses")
	newer := model.Stamp{WallMillis: time.Now().Add(time.Minute).UnixMilli(), Device: "d-other"}
	err := home.ws.ApplyRemoteCommit(context.Background(), journal.Commit{ID: "c-1", Device: "d-other", DeviceName: "other", Clock: newer,
		Ops: []journal.Op{{Kind: journal.OpModify, ObjectID: id, DriveID: "drive-v2", PreviousDriveID: "drive-v1",
			BodyID: make([]byte, 16), BodyMD5: "md5", Size: 12, ModTime: time.Now(), Version: newer}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := home.bodyOf(t, home.supersedeHolder(t)); got != "local edit that loses" {
		t.Fatalf("superseded edit reads %q", got)
	}
	home.expectNoBodyMoves(t)
}

func TestCrashMidAdoptionStillUnlocksAndKeepsTheSave(t *testing.T) {
	home := newTestHome(t)
	ctx := context.Background()
	keep := home.writeFile(t, "/", "notes.md", "v1")
	home.markSynced(t, keep, "drive-v1")
	if err := home.ws.Rename(ctx, rootIno, "notes.md", rootIno, "notes.md~", false); err != nil {
		t.Fatal(err)
	}
	absorbed := home.writeFile(t, "/", "notes.md", "v2 saved by vim")
	if err := home.ws.Unlink(ctx, rootIno, "notes.md~", fusefs.Caller{PID: 1}); err != nil {
		t.Fatal(err)
	}

	// AdoptReplacement's body move, then the process dies.
	if err := home.ws.moveAdoptedBody(ctx, keep, absorbed); err != nil {
		t.Fatal(err)
	}
	home.restart(t)

	if got := home.readFile(t, "/notes.md"); got != "v2 saved by vim" {
		t.Fatalf("after the crash notes.md reads %q", got)
	}
	if err := home.ws.AdoptReplacement(ctx, keep, absorbed); err != nil {
		t.Fatalf("adoption after the crash: %v", err)
	}
	if got := home.readFile(t, "/notes.md"); got != "v2 saved by vim" {
		t.Fatalf("after adoption notes.md reads %q", got)
	}
	if object := home.object(t, keep); object.State != model.Dirty || !isLive(object) {
		t.Fatalf("kept object = %+v", object)
	}
}
