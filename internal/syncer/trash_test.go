package syncer_test

import (
	"context"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/index"
)

func trashIDs(t *testing.T, d *devicetest.Device) map[string]string {
	t.Helper()
	ids := map[string]string{}
	d.Index.View(context.Background(), func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		for _, record := range records {
			ids[record.OriginalPath] = record.ID
		}
		return err
	})
	return ids
}

func TestTrashRestoreAndPurgeReachEveryDevice(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.Mkdir("/Photos")
	a.WriteFile("/Photos/cat.jpg", []byte("meow"))
	a.WriteFile("/report.pdf", []byte("q3"))
	a.Sync()
	a.Remove("/report.pdf")
	a.Remove("/Photos/cat.jpg")
	a.Remove("/Photos")
	a.Sync()
	b.Poll()
	if len(trashIDs(t, b)) != 2 {
		t.Fatalf("B trash = %v", trashIDs(t, b))
	}

	a.WriteFile("/report.pdf", []byte("a new report"))
	restored, err := a.Workspace.RestoreTrash(context.Background(), trashIDs(t, a)["/report.pdf"])
	if err != nil || restored != "/report (restored).pdf" {
		t.Fatalf("restore = %q, %v", restored, err)
	}
	if _, err := a.Workspace.PurgeTrash(context.Background(), trashIDs(t, a)["/Photos"]); err != nil {
		t.Fatal(err)
	}
	a.Sync()
	b.Poll()
	expectContent(t, b, "/report (restored).pdf", "q3")
	expectContent(t, b, "/report.pdf", "a new report")
	if len(trashIDs(t, b)) != 0 || len(driveFilesUnder(t, n, n.Folders.Trash)) != 0 {
		t.Fatalf("after purge B trash = %v, Drive trash = %d", trashIDs(t, b), len(driveFilesUnder(t, n, n.Folders.Trash)))
	}
}

func TestExpiredTrashIsPurgedOnceAcrossDevices(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/old.txt", []byte("old"))
	a.Sync()
	a.Remove("/old.txt")
	a.Sync()
	b.Poll()
	for _, d := range []*devicetest.Device{a, b} {
		if expired, err := d.Workspace.ExpireTrash(context.Background(), 0); err != nil || expired != 1 {
			t.Fatalf("%s expired %d, %v", d.ID, expired, err)
		}
	}
	a.Sync()
	b.SyncAndPoll()
	a.Poll()
	if len(driveFilesUnder(t, n, n.Folders.Trash)) != 0 {
		t.Fatal("expired item still on Drive")
	}
	purges := 0
	for _, commit := range ownCommits(t, a) {
		for _, op := range commit.Ops {
			if op.Kind == "trash_purge" {
				purges++
			}
		}
	}
	if purges != 1 {
		t.Fatalf("history holds %d purges of one item, want 1", purges)
	}
}
