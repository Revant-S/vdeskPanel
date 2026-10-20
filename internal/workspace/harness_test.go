package workspace

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

var testKeyring = sync.OnceValue(func() *crypto.Keyring {
	_, keyring, err := crypto.CreateVault([]byte("correct horse battery"), crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}, rand.Reader)
	if err != nil {
		panic(err)
	}
	return keyring
})

type offlineFetcher struct{}

func (offlineFetcher) FetchRange(context.Context, model.ObjectID, int64, int64, io.WriterAt) error {
	return errors.New("offline")
}

type testHome struct {
	dir   string
	index *index.Store
	cache *cache.Cache
	ws    *Workspace
	clock *journal.Clock
}

func newTestHome(t *testing.T) *testHome {
	t.Helper()
	home := &testHome{dir: t.TempDir()}
	home.open(t)
	return home
}

// open (re)opens the workspace on the same home directory, as a daemon restart would.
func (h *testHome) open(t *testing.T) {
	t.Helper()
	keyring := testKeyring()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := index.Open(context.Background(), filepath.Join(h.dir, "index.db"), crypto.NewIndexSealer(keyring, rand.Reader))
	if err != nil {
		t.Fatal(err)
	}
	content := func(use func(cipher.AEAD) error) error {
		return keyring.Use(func(k *crypto.Keys) error { return use(k.Content) })
	}
	bodies, err := cache.New(filepath.Join(h.dir, "cache"), cache.Deps{Fetcher: offlineFetcher{}, Bitmaps: store, Content: content, Random: rand.Reader, Logger: logger}, cache.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h.clock = journal.NewClock("d-test", time.Now, model.Stamp{}, nil, logger)
	ws, err := Open(context.Background(), Deps{Index: store, Cache: bodies, Names: crypto.NewKeyringNames(keyring, rand.Reader),
		Clock: h.clock, Random: rand.Reader, Now: time.Now, Logger: logger, DiskFree: StatFSOf(h.dir)},
		Config{UID: 1000, QuietPeriod: 50 * time.Millisecond, DeleteHold: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.index, h.cache, h.ws = store, bodies, ws
	t.Cleanup(h.close)
}

func (h *testHome) close() {
	if h.cache != nil {
		h.cache.Close()
		h.index.Close()
		h.cache, h.index = nil, nil
	}
}

func (h *testHome) restart(t *testing.T) {
	t.Helper()
	h.close()
	h.open(t)
}

func (h *testHome) inodeOfPath(t *testing.T, path string) model.Ino {
	t.Helper()
	entry, ok := h.ws.tree.Resolve(path)
	if !ok {
		t.Fatalf("%s not in tree", path)
	}
	return h.ws.inodes.inodeOf(entry.ID)
}

func (h *testHome) writeFile(t *testing.T, parentPath, name, content string) model.ObjectID {
	t.Helper()
	ctx := context.Background()
	attr, handle, err := h.ws.Create(ctx, h.inodeOfPath(t, parentPath), name, false, fusefs.Caller{PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteAt(ctx, []byte(content), 0); err != nil {
		t.Fatal(err)
	}
	if err := handle.Release(ctx); err != nil {
		t.Fatal(err)
	}
	id, _ := h.ws.inodes.objectOf(attr.Ino)
	return id
}

func (h *testHome) readFile(t *testing.T, path string) string {
	t.Helper()
	ctx := context.Background()
	handle, _, err := h.ws.Open(ctx, h.inodeOfPath(t, path), false, false, fusefs.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release(ctx)
	buf := make([]byte, 1<<20)
	n, err := handle.ReadAt(ctx, buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func (h *testHome) mkdir(t *testing.T, parentPath, name string) {
	t.Helper()
	if _, err := h.ws.Mkdir(context.Background(), h.inodeOfPath(t, parentPath), name); err != nil {
		t.Fatal(err)
	}
}

func (h *testHome) pendingOps(t *testing.T) []model.LocalOp {
	t.Helper()
	var out []model.LocalOp
	err := h.index.View(context.Background(), func(tx *index.Tx) error {
		ops, err := tx.UnbatchedOps()
		for _, op := range ops {
			var local model.LocalOp
			if err := json.Unmarshal(op.Payload, &local); err != nil {
				return err
			}
			out = append(out, local)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *testHome) object(t *testing.T, id model.ObjectID) index.Object {
	t.Helper()
	object, err := h.ws.indexObject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func opKinds(ops []model.LocalOp) []string {
	var kinds []string
	for _, op := range ops {
		kinds = append(kinds, string(op.Kind)+":"+op.Path)
	}
	return kinds
}

// clearPendingOps stands in for a completed sync.
func (h *testHome) clearPendingOps(t *testing.T) {
	t.Helper()
	err := h.index.Update(context.Background(), func(tx *index.Tx) error {
		ops, err := tx.UnbatchedOps()
		for _, op := range ops {
			if err := tx.DeleteOps([]int64{op.Seq}); err != nil {
				return err
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
