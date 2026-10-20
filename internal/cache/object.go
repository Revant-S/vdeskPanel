package cache

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/bits-and-blooms/bitset"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

// object is the shared state behind every open entry of one oid. It is either a complete body
// with any BodyID, or a sparse mirror of the remote body whose BodyID the index expects
// (arbitration R2). Only a mirror is ever range-fetched or repaired by refetching.
type object struct {
	cache *Cache
	id    model.ObjectID
	file  *os.File
	ctx   context.Context
	stop  context.CancelFunc

	// guarded by cache.mu
	refs    int
	writers int

	// fetchMu serializes range fetches and body replacement, so a fetch never lands in a new body.
	fetchMu sync.Mutex

	mu             sync.Mutex
	body           *vaultfile.Body
	size           int64
	expectedBodyID vaultfile.BodyID
	mirrorsRemote  bool
	present        *bitset.BitSet
	detached       bool
	prefetching    bool
	stopped        bool
	prefetches     sync.WaitGroup
}

func (c *Cache) newObject(id model.ObjectID, file *os.File) *object {
	ctx, stop := context.WithCancel(c.ctx)
	return &object{cache: c, id: id, file: file, ctx: ctx, stop: stop}
}

func (c *Cache) createBodyFile(id model.ObjectID, flags int) (*os.File, error) {
	path := c.BodyPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cache: create shard dir for %s: %w", id, err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cache: open body %s: %w", id, err)
	}
	return file, nil
}

func (c *Cache) createObject(ctx context.Context, id model.ObjectID) (*object, error) {
	file, err := c.createBodyFile(id, os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return nil, err
	}
	o := c.newObject(id, file)
	if err := o.startNewBody(ctx); err != nil {
		file.Close()
		return nil, err
	}
	return o, nil
}

func (c *Cache) openLocalObject(id model.ObjectID) (*object, error) {
	file, err := os.OpenFile(c.BodyPath(id), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cache: open %s: %w", id, ErrNoBody)
	}
	if err != nil {
		return nil, fmt.Errorf("cache: open body %s: %w", id, err)
	}
	o := c.newObject(id, file)
	if err := o.openCompleteBody(); err != nil {
		file.Close()
		return nil, err
	}
	return o, nil
}

// openCompleteBody takes the size from the file, since the index can lag a crash mid-write.
func (o *object) openCompleteBody() error {
	info, err := o.file.Stat()
	if err != nil {
		return fmt.Errorf("cache: stat body %s: %w", o.id, err)
	}
	size, err := vaultfile.PlaintextSize(info.Size())
	if err != nil {
		return fmt.Errorf("cache: body %s: %w", o.id, err)
	}
	var body *vaultfile.Body
	err = o.cache.withContent(func(content cipher.AEAD) error {
		body, err = vaultfile.OpenBody(o.file, content, size, o.cache.deps.Random)
		return err
	})
	if err != nil {
		return fmt.Errorf("cache: open body %s: %w", o.id, err)
	}
	o.body, o.size = body, size
	return nil
}

func (c *Cache) openSparseObject(ctx context.Context, request openRequest) (*object, error) {
	file, err := c.createBodyFile(request.id, os.O_CREATE)
	if err != nil {
		return nil, err
	}
	o := c.newObject(request.id, file)
	o.size, o.expectedBodyID, o.mirrorsRemote = request.size, request.bodyID, true
	if err := o.loadSparseState(ctx); err != nil {
		file.Close()
		return nil, err
	}
	return o, nil
}

// loadSparseState keeps what is on disk only if it belongs to the expected remote body;
// anything else is stale and starts over as an empty sparse file.
func (o *object) loadSparseState(ctx context.Context) error {
	stored, err := o.cache.deps.Bitmaps.SegmentBitmap(ctx, o.id)
	if err != nil {
		return fmt.Errorf("cache: load bitmap %s: %w", o.id, err)
	}
	body, err := o.openMatchingHeader()
	if err != nil {
		return err
	}
	if body == nil || stored == nil {
		if body != nil {
			body.Close()
		}
		return o.resetSparseFile(ctx)
	}
	o.body, o.present = body, stored
	return nil
}

func (o *object) openMatchingHeader() (*vaultfile.Body, error) {
	info, err := o.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("cache: stat body %s: %w", o.id, err)
	}
	if info.Size() != vaultfile.CiphertextSize(o.size) {
		return nil, nil
	}
	header, err := vaultfile.ReadHeader(o.file)
	if err != nil || header.BodyID != o.expectedBodyID {
		return nil, nil
	}
	return o.openBodyWithHeader()
}

func (o *object) openBodyWithHeader() (*vaultfile.Body, error) {
	var body *vaultfile.Body
	err := o.cache.withContent(func(content cipher.AEAD) error {
		var err error
		body, err = vaultfile.OpenBody(o.file, content, o.size, o.cache.deps.Random)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cache: open body %s: %w", o.id, err)
	}
	return body, nil
}

// resetSparseFile leaves a hole-filled file of the final ciphertext size, so fetched ranges
// land at their own offsets.
func (o *object) resetSparseFile(ctx context.Context) error {
	if err := o.file.Truncate(0); err != nil {
		return fmt.Errorf("cache: reset body %s: %w", o.id, err)
	}
	if err := o.file.Truncate(vaultfile.CiphertextSize(o.size)); err != nil {
		return fmt.Errorf("cache: size sparse body %s: %w", o.id, err)
	}
	o.present = bitset.New(uint(vaultfile.SegmentCount(o.size)))
	if err := o.cache.deps.Bitmaps.PutSegmentBitmap(ctx, o.id, o.present.Clone()); err != nil {
		return fmt.Errorf("cache: store bitmap %s: %w", o.id, err)
	}
	return nil
}

// startNewBody replaces whatever the file held with an empty body under a fresh file key.
// The caller holds fetchMu or owns the object exclusively.
func (o *object) startNewBody(ctx context.Context) error {
	if err := o.file.Truncate(0); err != nil {
		return fmt.Errorf("cache: truncate body %s: %w", o.id, err)
	}
	var body *vaultfile.Body
	err := o.cache.withContent(func(content cipher.AEAD) error {
		var err error
		body, err = vaultfile.NewBody(o.file, content, o.cache.deps.Random)
		return err
	})
	if err != nil {
		return fmt.Errorf("cache: new body %s: %w", o.id, err)
	}
	o.mu.Lock()
	previous := o.body
	o.body, o.size = body, 0
	o.mirrorsRemote, o.present = false, nil
	detached := o.detached
	o.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	if detached {
		return nil
	}
	return o.deleteBitmap(ctx)
}

// deleteBitmap marks the body as local-authored: no bitmap record means complete.
func (o *object) deleteBitmap(ctx context.Context) error {
	if err := o.cache.deps.Bitmaps.PutSegmentBitmap(ctx, o.id, nil); err != nil {
		return fmt.Errorf("cache: clear bitmap %s: %w", o.id, err)
	}
	return nil
}

func (o *object) complete() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.completeLocked()
}

func (o *object) completeLocked() bool {
	if !o.mirrorsRemote {
		return true
	}
	return o.body != nil && allPresent(o.present, vaultfile.SegmentCount(o.size))
}

func allPresent(present *bitset.BitSet, count int64) bool {
	if present == nil || present.Len() < uint(count) {
		return false
	}
	next, found := present.NextClear(0)
	return !found || next >= uint(count)
}

func (o *object) markDetached() {
	o.mu.Lock()
	o.detached = true
	o.mu.Unlock()
	o.stop()
}

func (o *object) stopPrefetch() {
	o.mu.Lock()
	o.stopped = true
	o.mu.Unlock()
	o.stop()
	o.prefetches.Wait()
}

func (o *object) close() {
	o.stopPrefetch()
	o.mu.Lock()
	if o.body != nil {
		o.body.Close()
	}
	o.mu.Unlock()
	o.file.Close()
}
