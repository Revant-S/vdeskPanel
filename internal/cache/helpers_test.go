package cache

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bits-and-blooms/bitset"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/secret"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

type fetchedRange struct {
	offset, length int64
}

type fakeRemote struct {
	mu      sync.Mutex
	bodies  map[model.ObjectID][]byte
	fetched []fetchedRange
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{bodies: map[model.ObjectID][]byte{}}
}

func (r *fakeRemote) FetchRange(_ context.Context, id model.ObjectID, offset, length int64, w io.WriterAt) error {
	r.mu.Lock()
	body, ok := r.bodies[id]
	r.fetched = append(r.fetched, fetchedRange{offset, length})
	r.mu.Unlock()
	if !ok || offset+length > int64(len(body)) {
		return fmt.Errorf("fake remote: range %d+%d of %s: %w", offset, length, id, os.ErrNotExist)
	}
	_, err := w.WriteAt(body[offset:offset+length], 0)
	return err
}

func (r *fakeRemote) takeFetched() []fetchedRange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.fetched
	r.fetched = nil
	return out
}

func (r *fakeRemote) waitForFetch(t *testing.T, want fetchedRange) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, got := range r.fetched {
			if got == want {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("range %+v never fetched", want)
}

type fakeBitmaps struct {
	mu     sync.Mutex
	stored map[model.ObjectID]*bitset.BitSet
}

func newFakeBitmaps() *fakeBitmaps {
	return &fakeBitmaps{stored: map[model.ObjectID]*bitset.BitSet{}}
}

func (b *fakeBitmaps) SegmentBitmap(_ context.Context, id model.ObjectID) (*bitset.BitSet, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if stored, ok := b.stored[id]; ok {
		return stored.Clone(), nil
	}
	return nil, nil
}

func (b *fakeBitmaps) PutSegmentBitmap(_ context.Context, id model.ObjectID, bitmap *bitset.BitSet) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bitmap == nil {
		delete(b.stored, id)
	} else {
		b.stored[id] = bitmap.Clone()
	}
	return nil
}

func (b *fakeBitmaps) has(id model.ObjectID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.stored[id]
	return ok
}

type testRig struct {
	dir     string
	content cipher.AEAD
	remote  *fakeRemote
	bitmaps *fakeBitmaps
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	key, err := secret.RandomKey(crypto.KeySize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Destroy)
	content, err := crypto.NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	return &testRig{dir: t.TempDir(), content: content, remote: newFakeRemote(), bitmaps: newFakeBitmaps()}
}

func (r *testRig) newCache(t *testing.T, runSegments int64) *Cache {
	t.Helper()
	deps := Deps{
		Fetcher: r.remote,
		Bitmaps: r.bitmaps,
		Content: func(use func(cipher.AEAD) error) error { return use(r.content) },
		Random:  rand.Reader,
	}
	c, err := New(r.dir, deps, Config{RunSegments: runSegments})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// publishRemoteBody seals plaintext as the remote copy of id and returns its BodyID.
func (r *testRig) publishRemoteBody(t *testing.T, id model.ObjectID, plaintext []byte) vaultfile.BodyID {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "remote"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := vaultfile.NewBody(file, r.content, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := body.WriteAt(plaintext, 0); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	r.remote.mu.Lock()
	r.remote.bodies[id] = ciphertext
	r.remote.mu.Unlock()
	return body.Header().BodyID
}

func patternedPlaintext(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i*7 + i/65536)
	}
	return out
}

func readRange(t *testing.T, entry *Entry, offset, length int64) []byte {
	t.Helper()
	buf := make([]byte, length)
	n, err := entry.ReadAt(context.Background(), buf, offset)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt(%d, %d): %v", offset, length, err)
	}
	return buf[:n]
}

func assertRange(t *testing.T, entry *Entry, plaintext []byte, offset, length int64) {
	t.Helper()
	if got := readRange(t, entry, offset, length); !bytes.Equal(got, plaintext[offset:offset+length]) {
		t.Fatalf("bytes at %d+%d differ", offset, length)
	}
}

func segmentRange(first, last int64, plaintextSize int64) fetchedRange {
	end := min(vaultfile.SegmentOffset(last+1), vaultfile.CiphertextSize(plaintextSize))
	return fetchedRange{vaultfile.SegmentOffset(first), end - vaultfile.SegmentOffset(first)}
}

var headerRange = fetchedRange{0, vaultfile.HeaderSize}

const testID model.ObjectID = "0123456789abcdef0123456789abcdef"
