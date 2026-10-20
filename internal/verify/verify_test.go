package verify_test

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/devicetest"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/remote"
	"github.com/cosmoboy/vault/internal/vaultfile"
	"github.com/cosmoboy/vault/internal/verify"
)

type fixture struct {
	t       *testing.T
	network *devicetest.Network
	device  *devicetest.Device
}

func newSyncedFixture(t *testing.T) *fixture {
	t.Helper()
	network := devicetest.NewNetwork(t)
	f := &fixture{t: t, network: network, device: network.NewDevice(t, "a")}
	d := f.device
	d.Mkdir("/docs")
	d.Mkdir("/docs/old")
	d.WriteFile("/notes.txt", []byte("shopping list"))
	d.WriteFile("/docs/report.pdf", make([]byte, 200<<10))
	d.WriteFile("/docs/old/letter.txt", []byte("dear"))
	d.WriteFile("/draft.txt", []byte("to be deleted"))
	d.Sync()
	d.Remove("/draft.txt")
	d.Sync()
	return f
}

func (f *fixture) verify(deep bool) verify.Report {
	f.t.Helper()
	d := f.device
	v := verify.New(verify.Deps{Index: d.Index, Remote: remote.Verify{Client: d.Client},
		Names: crypto.NewKeyringNames(f.network.Keyring, rand.Reader), Keyring: f.network.Keyring, Logger: f.network.Logger},
		verify.Config{TreeFolderID: f.network.Folders.Tree, TrashFolderID: f.network.Folders.Trash, TempDir: f.t.TempDir()})
	report, err := v.Verify(context.Background(), deep)
	if err != nil {
		f.t.Fatal(err)
	}
	return report
}

func (f *fixture) object(path string) index.Object {
	f.t.Helper()
	var object index.Object
	err := f.device.Index.View(context.Background(), func(tx *index.Tx) error {
		var err error
		object, err = tx.Object(f.device.ObjectID(path))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return object
}

func (f *fixture) editObject(path string, edit func(*index.Object)) {
	f.t.Helper()
	object := f.object(path)
	edit(&object)
	if err := f.device.Index.Update(context.Background(), func(tx *index.Tx) error { return tx.PutObject(object) }); err != nil {
		f.t.Fatal(err)
	}
}

func assertProblems(t *testing.T, report verify.Report, want ...verify.Problem) {
	t.Helper()
	if len(report.Problems) != len(want) {
		t.Fatalf("problems = %+v, want %+v", report.Problems, want)
	}
	for i := range want {
		if report.Problems[i] != want[i] {
			t.Fatalf("problem %d = %+v, want %+v", i, report.Problems[i], want[i])
		}
	}
}

func TestHealthyVaultPassesShallowAndDeep(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	shallow := f.verify(false)
	assertProblems(t, shallow)
	if shallow.FoldersListed != 3 || shallow.FilesChecked != 3 || shallow.TrashChecked != 1 || shallow.BodiesVerified != 0 {
		t.Fatalf("shallow report = %+v", shallow)
	}
	deep := f.verify(true)
	assertProblems(t, deep)
	if deep.BodiesVerified != 4 || deep.BytesDownloaded == 0 {
		t.Fatalf("deep report = %+v, want 3 tree bodies and 1 trash body", deep)
	}
}

func TestCorruptBodyIsFoundOnlyByDeepVerify(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	report := f.object("/docs/report.pdf")
	f.network.Drive.CorruptContent(report.DriveID, vaultfile.SegmentOffset(1)+7)
	assertProblems(t, f.verify(false))
	assertProblems(t, f.verify(true), verify.Problem{ID: report.ID, Path: "/docs/report.pdf", Kind: verify.FailedAuthentication})
}

func TestFileDeletedOnDriveIsReportedMissing(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	letter := f.object("/docs/old/letter.txt")
	if err := f.device.Client.Delete(context.Background(), letter.DriveID); err != nil {
		t.Fatal(err)
	}
	want := verify.Problem{ID: letter.ID, Path: "/docs/old/letter.txt", Kind: verify.MissingOnDrive}
	assertProblems(t, f.verify(false), want)
	assertProblems(t, f.verify(true), want)
}

func TestMissingFolderIsReportedOnce(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	old := f.object("/docs/old")
	if err := f.device.Client.Delete(context.Background(), old.DriveID); err != nil {
		t.Fatal(err)
	}
	assertProblems(t, f.verify(false), verify.Problem{ID: old.ID, Path: "/docs/old", Kind: verify.MissingOnDrive})
}

func TestTrashItemDeletedOnDriveIsReportedMissing(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	var trash index.TrashRecord
	err := f.device.Index.View(context.Background(), func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		if err == nil && len(records) == 1 {
			trash = records[0]
		}
		return err
	})
	if err != nil || trash.DriveID == "" {
		t.Fatalf("trash record = %+v, %v", trash, err)
	}
	if err := f.device.Client.Delete(context.Background(), trash.DriveID); err != nil {
		t.Fatal(err)
	}
	assertProblems(t, f.verify(false), verify.Problem{ID: trash.ObjectID, Path: "/draft.txt", Kind: verify.MissingOnDrive, Trashed: true})
}

func TestIndexDisagreeingWithDriveIsReported(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.editObject("/notes.txt", func(o *index.Object) { o.BodyMD5 = "00000000000000000000000000000000" })
	f.editObject("/docs/report.pdf", func(o *index.Object) { o.ContentMAC = make([]byte, len(o.ContentMAC)) })
	notes, report := f.object("/notes.txt"), f.object("/docs/report.pdf")
	assertProblems(t, f.verify(false), verify.Problem{ID: notes.ID, Path: "/notes.txt", Kind: verify.ChecksumMismatch})
	assertProblems(t, f.verify(true),
		verify.Problem{ID: report.ID, Path: "/docs/report.pdf", Kind: verify.IdentityMismatch},
		verify.Problem{ID: notes.ID, Path: "/notes.txt", Kind: verify.ChecksumMismatch})
}
