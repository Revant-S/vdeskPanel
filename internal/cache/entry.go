package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cosmoboy/vault/internal/vaultfile"
)

// Entry is one open handle on a cached body. Entries of the same oid share one object.
type Entry struct {
	object   *object
	writable bool

	mu            sync.Mutex
	closed        bool
	sequentialEnd int64
}

// newEntry runs under cache.mu, which guards the reference counts.
func (o *object) newEntry(writable bool) *Entry {
	o.refs++
	if writable {
		o.writers++
	}
	return &Entry{object: o, writable: writable}
}

func (e *Entry) checkOpen() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	return nil
}

func (e *Entry) checkWritable() error {
	if err := e.checkOpen(); err != nil {
		return err
	}
	if !e.writable {
		return ErrReadOnly
	}
	return nil
}

func (e *Entry) Size() int64 {
	return e.object.currentSize()
}

func (e *Entry) Complete() bool {
	return e.object.complete()
}

func (e *Entry) Sync() error {
	if err := e.checkOpen(); err != nil {
		return err
	}
	if err := e.object.file.Sync(); err != nil {
		return fmt.Errorf("cache: sync body %s: %w", e.object.id, err)
	}
	return nil
}

func (e *Entry) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.mu.Unlock()
	e.object.cache.closeEntry(e)
	return nil
}

func (c *Cache) closeEntry(e *Entry) {
	o := e.object
	c.mu.Lock()
	o.refs--
	if e.writable {
		o.writers--
	}
	last := o.refs == 0
	if last && c.objects[o.id] == o {
		delete(c.objects, o.id)
	}
	c.mu.Unlock()
	if last {
		o.close()
	}
}

// ReadAt fetches whatever the range is missing, then decrypts it. On sequential reads it also
// starts fetching the next run in the background.
func (e *Entry) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := e.checkOpen(); err != nil {
		return 0, err
	}
	o := e.object
	size := o.currentSize()
	if offset >= size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	first, last := vaultfile.SegmentSpan(offset, min(int64(len(p)), size-offset))
	n, err := o.readVerified(ctx, p, offset, first, last)
	if err == nil || errors.Is(err, io.EOF) {
		e.followSequentialRead(offset, n, last)
	}
	return n, err
}

func (e *Entry) followSequentialRead(offset int64, n int, last int64) {
	e.mu.Lock()
	sequential := offset == e.sequentialEnd
	e.sequentialEnd = offset + int64(n)
	e.mu.Unlock()
	if sequential {
		run := e.object.cache.config.RunSegments
		e.object.prefetchRun((last/run + 1) * run)
	}
}

// readVerified applies arbitration R3: a present segment failing authentication is refetched
// once if the body mirrors the remote copy; in a local-authored body it is corruption.
func (o *object) readVerified(ctx context.Context, p []byte, offset, first, last int64) (int, error) {
	refetched := map[int64]bool{}
	for {
		if !o.rangePresent(first, last) {
			if err := o.fetchSegments(ctx, first, last); err != nil {
				return 0, err
			}
		}
		body, mirror := o.currentBody()
		if body == nil {
			return 0, fmt.Errorf("cache: read %s: %w", o.id, ErrIncompleteBody)
		}
		n, err := body.ReadAt(p, offset)
		var segmentErr *vaultfile.SegmentError
		if !errors.As(err, &segmentErr) {
			return n, err
		}
		if !mirror {
			o.cache.deps.Logger.Warn("local body failed authentication", "oid", string(o.id), "segment", segmentErr.Index)
			return n, fmt.Errorf("cache: read %s: %w: %w", o.id, ErrCorruptBody, err)
		}
		if refetched[segmentErr.Index] {
			return n, fmt.Errorf("cache: read %s after refetch: %w", o.id, err)
		}
		refetched[segmentErr.Index] = true
		if err := o.dropSegment(ctx, segmentErr.Index); err != nil {
			return 0, err
		}
	}
}

func (o *object) rangePresent(first, last int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.mirrorsRemote {
		return true
	}
	if o.body == nil {
		return false
	}
	for index := first; index <= last; index++ {
		if !o.present.Test(uint(index)) {
			return false
		}
	}
	return true
}

func (o *object) currentBody() (*vaultfile.Body, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.body, o.mirrorsRemote
}

func (o *object) currentSize() int64 {
	body, _ := o.currentBody()
	if body != nil {
		return body.Size()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.size
}

// EnsureComplete downloads every missing segment; the workspace calls it before the first
// mutation of a CloudOnly or Partial file (arbitration R1).
func (e *Entry) EnsureComplete(ctx context.Context) error {
	if err := e.checkOpen(); err != nil {
		return err
	}
	if e.object.complete() {
		return nil
	}
	return e.object.fetchSegments(ctx, 0, e.object.segmentCount()-1)
}

func (e *Entry) WriteAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if err := e.checkWritable(); err != nil {
		return 0, err
	}
	body, err := e.object.prepareMutation(ctx)
	if err != nil {
		return 0, err
	}
	n, err := body.WriteAt(p, offset)
	if err != nil {
		return n, fmt.Errorf("cache: write %s: %w", e.object.id, err)
	}
	return n, nil
}

// Truncate to zero on an incomplete body starts a new body instead of fetching the old one.
func (e *Entry) Truncate(ctx context.Context, size int64) error {
	if err := e.checkWritable(); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("cache: truncate %s to %d: negative size", e.object.id, size)
	}
	body, err := e.object.prepareMutation(ctx)
	if errors.Is(err, ErrIncompleteBody) && size == 0 {
		return e.object.restartBody(ctx)
	}
	if err != nil {
		return err
	}
	if err := body.Truncate(size); err != nil {
		return fmt.Errorf("cache: truncate %s: %w", e.object.id, err)
	}
	return nil
}

// prepareMutation turns a complete mirror into a local-authored body under one lock, so a
// segment dropped concurrently cannot slip past the completeness check.
func (o *object) prepareMutation(ctx context.Context) (*vaultfile.Body, error) {
	o.mu.Lock()
	if !o.completeLocked() {
		o.mu.Unlock()
		return nil, fmt.Errorf("cache: mutate %s: %w", o.id, ErrIncompleteBody)
	}
	body, wasMirror, detached := o.body, o.mirrorsRemote, o.detached
	o.mirrorsRemote, o.present = false, nil
	o.mu.Unlock()
	if wasMirror && !detached {
		if err := o.deleteBitmap(ctx); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (o *object) restartBody(ctx context.Context) error {
	o.fetchMu.Lock()
	defer o.fetchMu.Unlock()
	return o.startNewBody(ctx)
}
