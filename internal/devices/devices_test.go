package devices

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/model"
)

type memoryRemote struct {
	mu     sync.Mutex
	nextID int
	files  map[string]model.RemoteFile
	data   map[string][]byte
}

func newMemoryRemote() *memoryRemote {
	return &memoryRemote{files: map[string]model.RemoteFile{}, data: map[string][]byte{}}
}

func (m *memoryRemote) ListFolder(_ context.Context, folderID string) ([]model.RemoteFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.RemoteFile
	for _, file := range m.files {
		if file.Parents[0] == folderID {
			out = append(out, file)
		}
	}
	return out, nil
}

func (m *memoryRemote) Download(_ context.Context, driveID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[driveID], nil
}

func (m *memoryRemote) Upload(_ context.Context, folderID, name string, _ map[string]string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprint(m.nextID)
	m.files[id] = model.RemoteFile{ID: id, Name: name, Parents: []string{folderID}}
	m.data[id] = data
	return nil
}

func (m *memoryRemote) Delete(_ context.Context, driveID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, driveID)
	return nil
}

func newKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	_, keyring, err := crypto.CreateVault([]byte("correct horse battery"), crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keyring.Destroy)
	return keyring
}

func newRegistry(remote Remote, keyring *crypto.Keyring, now *time.Time) *Registry {
	return New(Deps{Remote: remote, Keyring: keyring, Random: rand.Reader, Now: func() time.Time { return *now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, Config{VaultID: "v", FolderID: "devices"})
}

func TestPublishKeepsFirstSeenAndOneCopy(t *testing.T) {
	ctx, remote, keyring := context.Background(), newMemoryRemote(), newKeyring(t)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	a := newRegistry(remote, keyring, &now)
	if err := a.Publish(ctx, "d-a", func(r *Record) { r.Name = "laptop" }); err != nil {
		t.Fatal(err)
	}
	firstSeen := now
	now = now.Add(time.Hour)
	b := newRegistry(remote, keyring, &now)
	if err := b.Publish(ctx, "d-a", func(r *Record) { r.Retired = true }); err != nil {
		t.Fatal(err)
	}
	records, err := a.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := records["d-a"]
	if got.Name != "laptop" || !got.Retired || !got.FirstSeen.Equal(firstSeen) {
		t.Fatalf("record = %+v", got)
	}
	if len(remote.files) != 1 {
		t.Fatalf("%d record files on Drive, want 1", len(remote.files))
	}
}

func TestPublishSkipsUnchangedRecord(t *testing.T) {
	ctx, remote, keyring := context.Background(), newMemoryRemote(), newKeyring(t)
	now := time.Now()
	registry := newRegistry(remote, keyring, &now)
	rename := func(r *Record) { r.Name = "laptop" }
	registry.Publish(ctx, "d-a", rename)
	registry.Publish(ctx, "d-a", rename)
	if remote.nextID != 1 {
		t.Fatalf("%d uploads, want 1", remote.nextID)
	}
}

func TestRefreshTakesNewestCopyAndSkipsForeignRecords(t *testing.T) {
	ctx, remote, keyring := context.Background(), newMemoryRemote(), newKeyring(t)
	now := time.Now()
	registry := newRegistry(remote, keyring, &now)
	registry.upload(ctx, Record{ID: "d-a", Name: "old", Updated: now})
	registry.upload(ctx, Record{ID: "d-a", Name: "new", Updated: now.Add(time.Second)})
	other := newRegistry(remote, newKeyring(t), &now)
	other.upload(ctx, Record{ID: "d-x", Name: "foreign", Updated: now})
	remote.Upload(ctx, "devices", "d-y.vdev", nil, []byte("garbage"))

	records, err := registry.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records["d-a"].Name != "new" {
		t.Fatalf("records = %+v", records)
	}
}

func TestRecordCannotBeRenamedToAnotherDevice(t *testing.T) {
	ctx, remote, keyring := context.Background(), newMemoryRemote(), newKeyring(t)
	now := time.Now()
	registry := newRegistry(remote, keyring, &now)
	registry.upload(ctx, Record{ID: "d-a", Name: "laptop", Updated: now})
	for id, file := range remote.files {
		file.Name = "d-b.vdev"
		remote.files[id] = file
	}
	records, _ := registry.Refresh(ctx)
	if len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
}
