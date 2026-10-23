package syncer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/drive/drivefake"
	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
)

func driveFilesUnder(t *testing.T, n *devicetest.Network, parent string) []model.RemoteFile {
	t.Helper()
	client := n.Client(t, devicetest.FastDrive())
	files, err := client.ListChildren(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func ownCommits(t *testing.T, d *devicetest.Device) []journal.Commit {
	t.Helper()
	var commits []journal.Commit
	err := d.Index.View(context.Background(), func(tx *index.Tx) error {
		records, err := tx.CommitsBefore(model.Stamp{}, 1000)
		for _, record := range records {
			commit, err := d.Journal.OpenRecord(record)
			if err != nil {
				return err
			}
			commits = append(commits, commit)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return commits
}

func countRequests(n *devicetest.Network, kind drivefake.RequestKind, since int) int {
	count := 0
	for _, request := range n.Drive.Requests()[since:] {
		if request.Kind == kind {
			count++
		}
	}
	return count
}

func objectOf(t *testing.T, d *devicetest.Device, id model.ObjectID) index.Object {
	t.Helper()
	var object index.Object
	err := d.Index.View(context.Background(), func(tx *index.Tx) error {
		var err error
		object, err = tx.Object(id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func TestNestedTreeArrivesEncryptedAndCommitted(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.Mkdir("/Notes")
	a.Mkdir("/Notes/GATE")
	a.WriteFile("/Notes/GATE/graphs.md", []byte("dijkstra"))
	a.WriteFile("/top.txt", []byte("hello"))
	a.Sync()

	top := driveFilesUnder(t, n, n.Folders.Tree)
	if len(top) != 2 {
		t.Fatalf("tree root holds %d files", len(top))
	}
	for _, file := range top {
		if strings.Contains(file.Name, "Notes") || strings.Contains(file.Name, "top") {
			t.Fatalf("plaintext name on Drive: %s", file.Name)
		}
	}
	graphs := objectOf(t, a, a.ObjectID("/Notes/GATE/graphs.md"))
	gate := objectOf(t, a, a.ObjectID("/Notes/GATE"))
	remote, body, ok := n.Drive.File(graphs.DriveID)
	if !ok || remote.Parents[0] != gate.DriveID || bytes.Contains(body, []byte("dijkstra")) || remote.Properties["oid"] != string(graphs.ID) {
		t.Fatalf("graphs.md on Drive = %+v", remote)
	}
	if graphs.State != model.Local || graphs.BodyMD5 != remote.MD5 {
		t.Fatalf("graphs.md row = %+v", graphs)
	}
	commits := ownCommits(t, a)
	if len(commits) != 1 || len(commits[0].Ops) != 4 {
		t.Fatalf("commits = %+v", commits)
	}
	if got := string(a.ReadFile("/Notes/GATE/graphs.md")); got != "dijkstra" {
		t.Fatalf("read after sync %q", got)
	}
}

func TestRenameAndMoveOfLargeFolderAreMetadataOnly(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.Mkdir("/Photos")
	a.Mkdir("/Archive")
	for i := 0; i < 300; i++ {
		a.WriteFile("/Photos/p"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".jpg", []byte{byte(i)})
	}
	a.Sync()
	before := len(n.Drive.Requests())
	a.Rename("/Photos", "/Archive/Pictures")
	a.Sync()
	if uploads := countRequests(n, drivefake.KindUpload, before); uploads != 1 {
		t.Fatalf("folder move made %d uploads, want only the commit file", uploads)
	}
	pictures := objectOf(t, a, a.ObjectID("/Archive/Pictures"))
	archive := objectOf(t, a, a.ObjectID("/Archive"))
	if remote, _, _ := n.Drive.File(pictures.DriveID); remote.Parents[0] != archive.DriveID {
		t.Fatalf("Pictures parent on Drive = %v", remote.Parents)
	}
	commits := ownCommits(t, a)
	if op := commits[0].Ops[0]; op.Kind != journal.OpMove || op.From != "/Photos" || op.To != "/Archive/Pictures" {
		t.Fatalf("latest commit = %+v", commits[0].Ops)
	}
}

func TestEditorTempFilesNeverReachDrive(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.WriteFile("/doc.txt", []byte("v1"))
	a.WriteFile("/notes.md", []byte("n1"))
	a.Sync()
	doc, notes := a.ObjectID("/doc.txt"), a.ObjectID("/notes.md")
	before := len(n.Drive.Requests())

	a.WriteFile("/.goutputstream-ZX81", []byte("v2"))
	a.Rename("/.goutputstream-ZX81", "/doc.txt")
	a.WriteFile("/.notes.md.swp", []byte("swap"))
	a.Rename("/notes.md", "/notes.md~")
	a.WriteFile("/notes.md", []byte("n2"))
	a.Remove("/notes.md~")
	a.Remove("/.notes.md.swp")
	a.Sync()

	if a.ObjectID("/doc.txt") != doc || a.ObjectID("/notes.md") != notes {
		t.Fatal("a save changed a file's identity")
	}
	commit := ownCommits(t, a)[0]
	var kinds []string
	for _, op := range commit.Ops {
		kinds = append(kinds, string(op.Kind)+":"+op.Path)
	}
	if len(commit.Ops) != 2 || commit.Ops[0].Kind != journal.OpModify || commit.Ops[1].Kind != journal.OpModify {
		t.Fatalf("save commit ops = %v", kinds)
	}
	if trashed := driveFilesUnder(t, n, n.Folders.Trash); len(trashed) != 0 {
		t.Fatalf("saves left %d trash items", len(trashed))
	}
	for _, request := range n.Drive.Requests()[before:] {
		if strings.Contains(request.Path, "goutputstream") || strings.Contains(request.Path, "swp") {
			t.Fatalf("temp file name reached Drive: %s", request.Path)
		}
	}
	if string(a.ReadFile("/notes.md")) != "n2" || string(a.ReadFile("/doc.txt")) != "v2" {
		t.Fatal("saved content wrong")
	}
	tree := driveFilesUnder(t, n, n.Folders.Tree)
	if len(tree) != 2 {
		t.Fatalf("tree root holds %d files after saves, want 2 (old versions deleted)", len(tree))
	}
}

func TestDeletesGoToTrashAndFolderDeletesCoalesce(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.Mkdir("/Photos")
	for i := 0; i < 25; i++ {
		a.WriteFile("/Photos/"+string(rune('a'+i))+".jpg", []byte{byte(i)})
	}
	a.WriteFile("/report.pdf", []byte("q3"))
	a.Sync()
	for i := 0; i < 25; i++ {
		a.Remove("/Photos/" + string(rune('a'+i)) + ".jpg")
	}
	a.Remove("/Photos")
	a.Remove("/report.pdf")
	a.Sync()
	trashed := driveFilesUnder(t, n, n.Folders.Trash)
	if len(trashed) != 2 {
		t.Fatalf("trash holds %d items, want 2", len(trashed))
	}
	var records []index.TrashRecord
	a.Index.View(context.Background(), func(tx *index.Tx) error {
		var err error
		records, err = tx.TrashRecords()
		return err
	})
	for _, record := range records {
		if record.Folder && (record.FileCount != 25 || record.OriginalPath != "/Photos") {
			t.Fatalf("folder trash record = %+v", record)
		}
	}
	if len(records) != 2 {
		t.Fatalf("trash records = %+v", records)
	}
}

func TestCrashMidBatchReplaysWithoutDuplicates(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.Mkdir("/Docs")
	for i := 0; i < 6; i++ {
		a.WriteFile("/Docs/f"+string(rune('0'+i)), []byte(strings.Repeat("z", 100*i)))
	}
	n.Drive.InjectFault(drivefake.Fault{Match: func(r *http.Request) bool {
		return drivefake.KindOf(r) == drivefake.KindUpload
	}, Status: 500, Times: 1000})
	if err := a.Outgoing.FlushNow(context.Background()); err == nil {
		t.Fatal("sync succeeded with uploads failing")
	}
	n.Drive.ClearFaults()
	a.Restart()
	a.Sync()
	files := driveFilesUnder(t, n, objectOf(t, a, a.ObjectID("/Docs")).DriveID)
	if len(files) != 6 {
		t.Fatalf("Docs holds %d files after replay, want 6", len(files))
	}
	if commits := ownCommits(t, a); len(commits) != 1 {
		t.Fatalf("commits after replay = %d, want 1", len(commits))
	}
}

func TestDuplicateContentIsCopiedServerSide(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	video := make([]byte, 200_000)
	rand.Read(video)
	a.WriteFile("/clip.mp4", video)
	a.Sync()
	before := len(n.Drive.Requests())
	a.WriteFile("/clip copy.mp4", video)
	a.Sync()
	if uploads := countRequests(n, drivefake.KindUpload, before); uploads != 1 {
		t.Fatalf("copy made %d uploads, want only the commit", uploads)
	}
	if !bytes.Equal(a.ReadFile("/clip copy.mp4"), video) {
		t.Fatal("copied file content differs")
	}
}

func TestFileDriveKeepsRefusingDoesNotBlockOthers(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	a.WriteFile("/stuck.bin", bytes.Repeat([]byte("s"), 6<<20))
	for i := 0; i < 5; i++ {
		a.WriteFile("/ok-"+string(rune('a'+i))+".txt", []byte("fine"))
	}
	isChunk := func(r *http.Request) bool {
		return r.Method == http.MethodPut && drivefake.KindOf(r) == drivefake.KindUpload
	}
	n.Drive.InjectFault(drivefake.Fault{Match: isChunk, Status: 500, Reason: "internalError", Times: 1000000})
	for attempt := 0; attempt < 4; attempt++ {
		a.Outgoing.FlushNow(context.Background())
	}
	tree := driveFilesUnder(t, n, n.Folders.Tree)
	if len(tree) != 5 {
		t.Fatalf("Drive holds %d files, want the 5 that Drive accepts", len(tree))
	}
	if files, _, _ := a.Outgoing.Backlog(context.Background()); files != 1 {
		t.Fatalf("backlog = %d, want only the refused file", files)
	}
}
