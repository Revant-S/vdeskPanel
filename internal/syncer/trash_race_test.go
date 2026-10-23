package syncer_test

import (
	"context"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/index"
)

func restoreOrFail(t *testing.T, d *devicetest.Device, originalPath string) string {
	t.Helper()
	restored, err := d.Workspace.RestoreTrash(context.Background(), trashIDs(t, d)[originalPath])
	if err != nil {
		t.Fatalf("%s: restore %s: %v", d.ID, originalPath, err)
	}
	return restored
}

func trashCount(t *testing.T, d *devicetest.Device) (count int) {
	t.Helper()
	d.Index.View(context.Background(), func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		count = len(records)
		return err
	})
	return count
}

func TestRestoreThenRenameBeforeSyncReachesOtherDevices(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/notes.txt", []byte("keep me"))
	a.Sync()
	a.Remove("/notes.txt")
	a.Sync()
	b.Poll()
	restoreOrFail(t, a, "/notes.txt")
	a.Rename("/notes.txt", "/renamed.txt")
	a.Sync()
	b.Poll()
	expectContent(t, b, "/renamed.txt", "keep me")
	if len(trashIDs(t, b)) != 0 || len(driveFilesUnder(t, n, n.Folders.Trash)) != 0 {
		t.Fatalf("B trash = %v, Drive trash = %d", trashIDs(t, b), len(driveFilesUnder(t, n, n.Folders.Trash)))
	}
	fresh := n.NewDevice(t, "fresh")
	fresh.Poll()
	expectContent(t, fresh, "/renamed.txt", "keep me")
}

func TestRestoreThenDeleteAgainBeforeSyncLeavesOneTrashItem(t *testing.T) {
	ctx := context.Background()
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/notes.txt", []byte("keep me"))
	a.Sync()
	a.Remove("/notes.txt")
	a.Sync()
	b.Poll()
	restoreOrFail(t, a, "/notes.txt")
	a.Remove("/notes.txt")
	a.Sync()
	b.Poll()
	if trashCount(t, a) != 1 || trashCount(t, b) != 1 {
		t.Fatalf("A trash = %d items, B trash = %d items, want one each", trashCount(t, a), trashCount(t, b))
	}
	if _, err := b.Workspace.PurgeTrash(ctx, trashIDs(t, b)["/notes.txt"]); err != nil {
		t.Fatal(err)
	}
	b.Sync()
	a.Poll()
	if trashCount(t, a) != 0 || len(driveFilesUnder(t, n, n.Folders.Trash)) != 0 {
		t.Fatalf("after B's purge A trash = %d items, Drive trash = %d", trashCount(t, a), len(driveFilesUnder(t, n, n.Folders.Trash)))
	}
}

func TestRestoreThenEditBeforeSyncReachesOtherDevices(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/notes.txt", []byte("old"))
	a.Sync()
	a.Remove("/notes.txt")
	a.Sync()
	b.Poll()
	restoreOrFail(t, a, "/notes.txt")
	a.WriteFile("/notes.txt", []byte("edited after restore"))
	a.Sync()
	b.Poll()
	expectContent(t, b, "/notes.txt", "edited after restore")
	if len(trashIDs(t, b)) != 0 {
		t.Fatalf("B trash = %v", trashIDs(t, b))
	}
}
