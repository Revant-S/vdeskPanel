package cache

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

// slowRemote holds each fetch briefly so parallel requests overlap, and can cancel the caller
// after a number of segment fetches.
type slowRemote struct {
	inner       *fakeRemote
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	segments    int
	cancelAfter int
	cancel      context.CancelFunc
}

func (r *slowRemote) FetchRange(ctx context.Context, id model.ObjectID, offset, length int64, w io.WriterAt) error {
	r.mu.Lock()
	r.inFlight++
	r.maxInFlight = max(r.maxInFlight, r.inFlight)
	if offset > 0 {
		r.segments++
		if r.segments == r.cancelAfter && r.cancel != nil {
			r.cancel()
		}
	}
	r.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	err := r.inner.FetchRange(ctx, id, offset, length, w)
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	return err
}

func (r *testRig) newCacheWith(t *testing.T, fetcher SegmentFetcher, runSegments int64) *Cache {
	t.Helper()
	deps := Deps{Fetcher: fetcher, Bitmaps: r.bitmaps, Random: rand.Reader,
		Content: func(use func(cipher.AEAD) error) error { return use(r.content) }}
	c, err := New(r.dir, deps, Config{RunSegments: runSegments})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestEnsureCompleteParallelFetchesRunsConcurrently(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	size := int64(len(plaintext))
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	remote := &slowRemote{inner: rig.remote}
	c := rig.newCacheWith(t, remote, 2)
	entry, err := c.Open(context.Background(), testID, size, model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	if err := entry.EnsureCompleteParallel(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if !entry.Complete() {
		t.Fatal("not complete")
	}
	fetched := rig.remote.takeFetched()
	assertRange(t, entry, plaintext, 0, size)
	if remote.maxInFlight < 2 || remote.maxInFlight > 4 {
		t.Fatalf("max in-flight range fetches = %d, want 2..4", remote.maxInFlight)
	}
	if len(fetched) != 7 {
		t.Fatalf("fetched %d ranges, want header + 6 runs: %v", len(fetched), fetched)
	}
	for _, got := range fetched[1:] {
		if got.length > segmentRange(0, 1, size).length {
			t.Fatalf("range %+v is longer than one run", got)
		}
	}
}

func TestEnsureCompleteParallelResumesWithoutRefetching(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	size := int64(len(plaintext))
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	ctx, cancel := context.WithCancel(context.Background())
	remote := &slowRemote{inner: rig.remote, cancelAfter: 2, cancel: cancel}
	first := rig.newCacheWith(t, remote, 2)
	entry, err := first.Open(ctx, testID, size, model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.EnsureCompleteParallel(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted fetch = %v, want context.Canceled", err)
	}
	entry.Close()
	first.Close()
	before := rig.remote.takeFetched()
	if len(before) >= 7 {
		t.Fatalf("interrupted fetch still fetched %d ranges", len(before))
	}

	second := rig.newCacheWith(t, rig.remote, 2)
	entry, err = second.Open(context.Background(), testID, size, model.Partial, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	if err := entry.EnsureCompleteParallel(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	assertRange(t, entry, plaintext, 0, size)
	seen := map[fetchedRange]bool{}
	for _, got := range append(before, rig.remote.takeFetched()...) {
		if seen[got] {
			t.Fatalf("range %+v fetched twice", got)
		}
		seen[got] = true
	}
}
