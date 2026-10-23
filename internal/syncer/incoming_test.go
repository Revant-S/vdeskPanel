package syncer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vault/internal/devicetest"
	"vault/internal/drive/drivefake"
	"vault/internal/index"
	"vault/internal/model"
)

func trashReasons(t *testing.T, d *devicetest.Device) []string {
	t.Helper()
	var reasons []string
	d.Index.View(context.Background(), func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		for _, record := range records {
			reasons = append(reasons, record.OriginalPath+"|"+record.Reason)
		}
		return err
	})
	slices.Sort(reasons)
	return reasons
}

func expectContent(t *testing.T, d *devicetest.Device, path, want string) {
	t.Helper()
	if got := string(d.ReadFile(path)); got != want {
		t.Fatalf("%s: %s = %q, want %q", d.ID, path, got, want)
	}
}

func TestJoiningDeviceSeesWholeTreeWithoutDownloading(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.Mkdir("/Notes")
	big := make([]byte, 300_000)
	rand.Read(big)
	a.WriteFile("/Notes/gate.md", []byte("graphs"))
	a.WriteFile("/Notes/big.bin", big)
	a.Rename("/Notes/gate.md", "/Notes/gate-2026.md")
	a.Sync()

	before := len(n.Drive.Requests())
	b := n.NewDevice(t, "b")
	b.Poll()
	if downloads := countRequests(n, drivefake.KindDownload, before); downloads != countLogDownloads(n, before) {
		t.Fatalf("join downloaded %d bodies", downloads-countLogDownloads(n, before))
	}
	if !b.Exists("/Notes/gate-2026.md") || b.Exists("/Notes/gate.md") {
		t.Fatal("B's tree differs from A's")
	}
	if objectOf(t, b, b.ObjectID("/Notes/big.bin")).State != model.CloudOnly {
		t.Fatal("joined file is not cloud only")
	}
	expectContent(t, b, "/Notes/gate-2026.md", "graphs")
	if !bytes.Equal(b.ReadFile("/Notes/big.bin"), big) {
		t.Fatal("big.bin differs on B")
	}
}

func countLogDownloads(n *devicetest.Network, since int) int {
	count := 0
	for _, request := range n.Drive.Requests()[since:] {
		if request.Kind == drivefake.KindDownload && request.Offset < 0 {
			count++
		}
	}
	return count
}

func TestEditOnOneDeviceReachesTheOther(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/doc.txt", []byte("v1"))
	a.Sync()
	b.Poll()
	expectContent(t, b, "/doc.txt", "v1")
	a.WriteFile("/doc.txt", []byte("v2 from a"))
	a.Sync()
	b.Poll()
	expectContent(t, b, "/doc.txt", "v2 from a")
	b.WriteFile("/doc.txt", []byte("v3 from b"))
	b.SyncAndPoll()
	a.Poll()
	expectContent(t, a, "/doc.txt", "v3 from b")
	if reasons := trashReasons(t, a); len(reasons) != 0 {
		t.Fatalf("sequential edits created trash: %v", reasons)
	}
	if files := driveFilesUnder(t, n, n.Folders.Tree); len(files) != 1 {
		t.Fatalf("Drive tree holds %d versions, want 1", len(files))
	}
}

func TestUnsyncedEditLosingToNewerRemoteEditGoesToTrash(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	a.WriteFile("/doc.txt", []byte("older edit on a"))
	b.WriteFile("/doc.txt", []byte("newer edit on b"))
	b.Sync()
	a.Poll()
	a.Sync()
	b.Poll()
	expectContent(t, a, "/doc.txt", "newer edit on b")
	expectContent(t, b, "/doc.txt", "newer edit on b")
	for _, d := range []*devicetest.Device{a, b} {
		reasons := trashReasons(t, d)
		if len(reasons) != 1 || reasons[0] != "/doc.txt|superseded by d-b" {
			t.Fatalf("%s trash = %v", d.ID, reasons)
		}
	}
	assertTrashHolds(t, n, "older edit on a")
	if files, _, err := a.Outgoing.Backlog(context.Background()); err != nil || files != 0 {
		t.Fatalf("a still counts %d files not backed up (%v) after its version went to trash", files, err)
	}
}

func TestNewerEditUploadedSecondStillWins(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	b.WriteFile("/doc.txt", []byte("older edit on b"))
	a.WriteFile("/doc.txt", []byte("newer edit on a"))
	b.Sync()
	a.Poll()
	a.Sync()
	b.Poll()
	b.Sync()
	a.Poll()
	expectContent(t, a, "/doc.txt", "newer edit on a")
	expectContent(t, b, "/doc.txt", "newer edit on a")
	assertTrashHolds(t, n, "older edit on b")
	if files := driveFilesUnder(t, n, n.Folders.Tree); len(files) != 1 {
		t.Fatalf("Drive tree holds %d versions, want 1", len(files))
	}
}

// assertTrashHolds downloads every trash body and checks one decrypts to want.
func assertTrashHolds(t *testing.T, n *devicetest.Network, want string) {
	t.Helper()
	viewer := n.NewDevice(t, "viewer")
	viewer.Poll()
	for _, item := range driveFilesUnder(t, n, n.Folders.Trash) {
		if item.Folder {
			for _, inner := range driveFilesUnder(t, n, item.ID) {
				if !inner.Folder && string(viewer.DecryptRemoteBody(inner.ID)) == want {
					return
				}
			}
			continue
		}
		if plaintext := viewer.DecryptRemoteBody(item.ID); string(plaintext) == want {
			return
		}
	}
	t.Fatalf("no trash item holds %q", want)
}

func TestRenameOnOneDeviceAndEditOnOtherBothApply(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/draft.txt", []byte("v1"))
	a.Sync()
	b.Poll()
	a.Rename("/draft.txt", "/final.txt")
	b.WriteFile("/draft.txt", []byte("v2"))
	a.Sync()
	b.SyncAndPoll()
	a.Poll()
	for _, d := range []*devicetest.Device{a, b} {
		if !d.Exists("/final.txt") || d.Exists("/draft.txt") {
			t.Fatalf("%s: rename not applied", d.ID)
		}
		expectContent(t, d, "/final.txt", "v2")
	}
}

func TestNewerEditBeatsOlderDeleteAndOlderEditLosesToNewerDelete(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/keep.txt", []byte("base"))
	a.WriteFile("/gone.txt", []byte("base"))
	a.Sync()
	b.Poll()
	a.Remove("/keep.txt")
	b.WriteFile("/keep.txt", []byte("edited after the delete"))
	b.WriteFile("/gone.txt", []byte("edited before the delete"))
	a.Remove("/gone.txt")
	a.Sync()
	b.SyncAndPoll()
	b.Sync()
	a.Poll()
	for _, d := range []*devicetest.Device{a, b} {
		if !d.Exists("/keep.txt") {
			t.Fatalf("%s: newer edit did not survive the delete", d.ID)
		}
		expectContent(t, d, "/keep.txt", "edited after the delete")
		if d.Exists("/gone.txt") {
			t.Fatalf("%s: older edit resurrected a newer delete", d.ID)
		}
	}
	assertTrashHolds(t, n, "edited before the delete")
}

func TestFolderDeletedWhileOtherDeviceAddsInsideKeepsEverythingInTrash(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.Mkdir("/Project")
	a.WriteFile("/Project/plan.md", []byte("plan"))
	a.Sync()
	b.Poll()
	a.Remove("/Project/plan.md")
	a.Remove("/Project")
	b.WriteFile("/Project/notes.md", []byte("written inside on b"))
	a.Sync()
	b.SyncAndPoll()
	a.SyncAndPoll()
	b.SyncAndPoll()
	for _, d := range []*devicetest.Device{a, b} {
		if d.Exists("/Project") {
			t.Fatalf("%s: deleted folder is back", d.ID)
		}
	}
	assertTrashHolds(t, n, "written inside on b")
}

func TestSameNameCreatedOnBothDevicesConverges(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/todo.txt", []byte("from a"))
	b.WriteFile("/todo.txt", []byte("from b"))
	a.Sync()
	b.SyncAndPoll()
	a.SyncAndPoll()
	b.SyncAndPoll()
	var names [2][]string
	for i, d := range []*devicetest.Device{a, b} {
		for _, entry := range d.Workspace.Tree().Children(model.RootID) {
			names[i] = append(names[i], entry.Name)
		}
		slices.Sort(names[i])
	}
	if !slices.Equal(names[0], names[1]) || len(names[0]) != 2 || !strings.Contains(strings.Join(names[0], ","), "conflict") {
		t.Fatalf("a = %v, b = %v", names[0], names[1])
	}
}

func TestExpiredPageTokenRebuildsWithoutDuplicates(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/one.txt", []byte("1"))
	a.Sync()
	b.Poll()
	n.Drive.ExpirePageTokens()
	a.WriteFile("/two.txt", []byte("2"))
	a.Sync()
	b.Poll()
	if !b.Exists("/one.txt") || !b.Exists("/two.txt") || len(b.Workspace.Tree().Children(model.RootID)) != 2 {
		t.Fatal("rebuild after token expiry lost or duplicated files")
	}
}

func TestNetworkDropMidSyncCompletesWithoutDuplicates(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.Mkdir("/Docs")
	for i := 0; i < 20; i++ {
		a.WriteFile("/Docs/f"+string(rune('a'+i)), []byte(strings.Repeat("d", 1000*i)))
	}
	var sent atomic.Int32
	n.Drive.InjectFault(drivefake.Fault{Match: func(*http.Request) bool { return sent.Add(1) > 15 }, Status: 503, Times: 100000})
	if err := a.Outgoing.FlushNow(context.Background()); err == nil {
		t.Fatal("sync succeeded with the network down")
	}
	n.Drive.ClearFaults()
	a.Sync()
	b.Poll()
	docs := objectOf(t, a, a.ObjectID("/Docs"))
	if files := driveFilesUnder(t, n, docs.DriveID); len(files) != 20 {
		t.Fatalf("Drive holds %d files after the drop, want 20", len(files))
	}
	if got := len(b.Workspace.Tree().Children(b.ObjectID("/Docs"))); got != 20 {
		t.Fatalf("B sees %d files, want 20", got)
	}
	expectContent(t, b, "/Docs/fc", strings.Repeat("d", 2000))
}

func TestRestartSettlesTrashEntriesOlderBuildsLeftDirty(t *testing.T) {
	ctx := context.Background()
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	a.WriteFile("/doc.txt", []byte("older edit on a"))
	b.WriteFile("/doc.txt", []byte("newer edit on b"))
	b.Sync()
	a.Poll()
	a.Sync()
	err := a.Index.Update(ctx, func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		if err != nil || len(records) != 1 {
			return fmt.Errorf("trash records %v: %w", records, err)
		}
		object, err := tx.Object(records[0].ObjectID)
		if err != nil {
			return err
		}
		object.State = model.Dirty
		return tx.PutObject(object)
	})
	if err != nil {
		t.Fatal(err)
	}
	a.Restart()
	if files, _, err := a.Outgoing.Backlog(ctx); err != nil || files != 0 {
		t.Fatalf("after restart a counts %d files not backed up (%v)", files, err)
	}
}

func TestPendingAttributeChangeIsNotAnUnsyncedEdit(t *testing.T) {
	ctx := context.Background()
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.WriteFile("/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	a.Touch("/doc.txt", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	b.WriteFile("/doc.txt", []byte("newer edit on b"))
	b.Sync()
	a.Poll()
	a.Sync()
	expectContent(t, a, "/doc.txt", "newer edit on b")
	if reasons := trashReasons(t, a); len(reasons) != 0 {
		t.Fatalf("a touched file went to trash as an edit: %v", reasons)
	}
	if files, _, err := a.Outgoing.Backlog(ctx); err != nil || files != 0 {
		t.Fatalf("a counts %d files not backed up (%v)", files, err)
	}
}
