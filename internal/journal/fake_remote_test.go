package journal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

var errGone = errors.New("fake: not found")

type fakeLogRemote struct {
	mu          sync.Mutex
	files       map[string]LogFile
	nextID      int
	failCreates int
}

func newFakeLogRemote() *fakeLogRemote {
	return &fakeLogRemote{files: map[string]LogFile{}}
}

func (f *fakeLogRemote) GenerateID(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	return fmt.Sprintf("gen-%d", f.nextID), nil
}

// CreateFile stores the file, then fails if asked to: the upload landed but the caller never heard.
func (f *fakeLogRemote) CreateFile(_ context.Context, file LogFile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[file.DriveID]; ok {
		return false, nil
	}
	f.files[file.DriveID] = file
	if f.failCreates > 0 {
		f.failCreates--
		return true, errors.New("fake: connection reset after upload")
	}
	return true, nil
}

func (f *fakeLogRemote) Exists(_ context.Context, driveID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[driveID]
	return ok, nil
}

func (f *fakeLogRemote) Download(_ context.Context, driveID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[driveID]
	if !ok {
		return nil, errGone
	}
	return file.Data, nil
}

func (f *fakeLogRemote) ListFolder(_ context.Context, folderID string) ([]model.RemoteFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.RemoteFile
	for id, file := range f.files {
		if file.FolderID == folderID {
			out = append(out, model.RemoteFile{ID: id, Name: file.Name, Parents: []string{folderID}})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (f *fakeLogRemote) Delete(_ context.Context, driveID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, driveID)
	return nil
}

type testDevice struct {
	journal *Journal
	store   *index.Store
	remote  *fakeLogRemote
	folder  string
}

var sharedKeyring = func() func(t *testing.T) *crypto.Keyring {
	var once sync.Once
	var keyring *crypto.Keyring
	return func(t *testing.T) *crypto.Keyring {
		once.Do(func() {
			var err error
			_, keyring, err = crypto.CreateVault([]byte("correct horse battery"), crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
		})
		return keyring
	}
}()

func newTestDevice(t *testing.T, remote *fakeLogRemote, device model.DeviceID, packEvery int, wall func() time.Time) *testDevice {
	t.Helper()
	keyring := sharedKeyring(t)
	store, err := index.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"), crypto.NewIndexSealer(keyring, rand.Reader))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	codec, err := NewCodec(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(codec.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	folder := "log-" + string(device)
	j := New(Deps{Remote: remote, Store: store, Keyring: keyring, Codec: codec,
		Clock: NewClock(device, wall, model.Stamp{}, nil, logger), Logger: logger},
		Config{VaultID: "vault", Device: device, DeviceName: string(device) + "-laptop", OwnLogFolderID: folder, PackEvery: packEvery})
	return &testDevice{journal: j, store: store, remote: remote, folder: folder}
}

func (d *testDevice) record(t *testing.T, ops ...Op) Commit {
	t.Helper()
	commitID, err := NewCommitID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	driveID, _ := d.remote.GenerateID(context.Background())
	commit, err := d.journal.Record(context.Background(), commitID, driveID, ops)
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func (d *testDevice) ingestFrom(t *testing.T, other *testDevice) []Commit {
	t.Helper()
	files, _ := d.remote.ListFolder(context.Background(), other.folder)
	commits, err := d.journal.Ingest(context.Background(), files, func(err error) bool { return errors.Is(err, errGone) })
	if err != nil {
		t.Fatal(err)
	}
	return commits
}

func (d *testDevice) markAllApplied(t *testing.T) {
	t.Helper()
	err := d.store.Update(context.Background(), func(tx *index.Tx) error {
		unapplied, err := tx.UnappliedCommits()
		for _, c := range unapplied {
			if err := tx.MarkCommitApplied(c.ID); err != nil {
				return err
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func steppingWall() func() time.Time {
	var mu sync.Mutex
	now := time.UnixMilli(1_761_055_200_000)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Millisecond)
		return now
	}
}
