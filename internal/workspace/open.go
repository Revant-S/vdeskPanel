package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

type handle struct {
	ws        *Workspace
	id        model.ObjectID
	entry     *cache.Entry
	writable  bool
	processID uint32
	detached  atomic.Bool
	wrote     atomic.Bool
	mu        sync.Mutex
}

func (w *Workspace) Open(ctx context.Context, ino model.Ino, writable, truncate bool, caller fusefs.Caller) (fusefs.Handle, bool, error) {
	entry, err := w.entryOf(ino)
	if err != nil {
		return nil, false, err
	}
	if entry.IsFolder() {
		return nil, false, syscall.EISDIR
	}
	object, err := w.indexObject(ctx, entry.ID)
	if err != nil {
		return nil, false, err
	}
	remoteOnly := object.State == model.CloudOnly || object.State == model.Partial
	if remoteOnly && w.DenyRead != nil && w.DenyRead(caller.PID) {
		return nil, false, syscall.EIO
	}
	cacheEntry, err := w.openCacheEntry(ctx, object, writable)
	if err != nil {
		return nil, false, err
	}
	h := &handle{ws: w, id: entry.ID, entry: cacheEntry, writable: writable, processID: caller.PID}
	w.handles.add(h)
	if truncate && writable {
		if err := h.truncate(ctx, 0); err != nil {
			h.Release(ctx)
			return nil, false, err
		}
	}
	keepCache := !writable && !remoteOnly
	if keepCache && w.Reads != nil {
		w.Reads.ObserveCachedOpen(entry.ID, caller.PID)
	}
	return h, keepCache, nil
}

func (w *Workspace) openCacheEntry(ctx context.Context, object index.Object, writable bool) (*cache.Entry, error) {
	var bodyID vaultfile.BodyID
	copy(bodyID[:], object.BodyID)
	open := w.Cache.Open
	if writable {
		open = w.Cache.OpenWritable
	}
	entry, err := open(ctx, object.ID, object.Size, object.State, bodyID)
	if err != nil {
		return nil, fmt.Errorf("open body %s: %w", object.ID, err)
	}
	return entry, nil
}

func (w *Workspace) indexObject(ctx context.Context, id model.ObjectID) (index.Object, error) {
	var object index.Object
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		var err error
		object, err = tx.Object(id)
		return err
	})
	if errors.Is(err, index.ErrNotFound) {
		return index.Object{}, errNotIndexed
	}
	return object, err
}

var errNotIndexed = fmt.Errorf("workspace: object not in index: %w", fs.ErrNotExist)

func (h *handle) ReadAt(ctx context.Context, p []byte, offset int64) (int, error) {
	n, err := h.entry.ReadAt(ctx, p, offset)
	if h.ws.Reads != nil && n > 0 {
		h.ws.Reads.ObserveRead(h.ws.handles.objectOf(h), h.processID, offset, int64(n), h.entry.Size())
	}
	return n, cacheError(err)
}

// WriteAt on a detached handle succeeds without effect on the vault, like writing to an
// unlinked file.
func (h *handle) WriteAt(ctx context.Context, p []byte, offset int64) (int, error) {
	if h.detached.Load() {
		return len(p), nil
	}
	if err := h.prepareMutation(ctx); err != nil {
		return 0, err
	}
	n, err := h.entry.WriteAt(ctx, p, offset)
	if err != nil {
		return n, cacheError(err)
	}
	h.ws.noteLocalWrite(h.ws.handles.objectOf(h), h.entry.Size())
	return n, nil
}

func (h *handle) truncate(ctx context.Context, size int64) error {
	if h.detached.Load() {
		return nil
	}
	if size > 0 {
		if err := h.prepareMutation(ctx); err != nil {
			return err
		}
	} else if err := h.markWritten(ctx); err != nil {
		return err
	}
	if err := h.entry.Truncate(ctx, size); err != nil {
		return cacheError(err)
	}
	h.ws.noteLocalWrite(h.ws.handles.objectOf(h), size)
	return nil
}

// prepareMutation downloads the whole body before the first change, because a body with
// holes must never be uploaded.
func (h *handle) prepareMutation(ctx context.Context) error {
	if !h.entry.Complete() {
		if err := h.entry.EnsureComplete(ctx); err != nil {
			return cacheError(err)
		}
	}
	return h.markWritten(ctx)
}

func (h *handle) markWritten(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.wrote.Load() {
		return nil
	}
	if err := h.ws.markDirty(ctx, h.ws.handles.objectOf(h)); err != nil {
		return err
	}
	h.wrote.Store(true)
	return nil
}

func (h *handle) Sync(context.Context) error {
	return cacheError(h.entry.Sync())
}

func (h *handle) Release(ctx context.Context) error {
	id := h.ws.handles.objectOf(h)
	closeErr := h.entry.Close()
	h.ws.handles.remove(h)
	wrote := h.wrote.Load() && !h.detached.Load()
	if wrote {
		if err := h.ws.recordModify(ctx, id); err != nil {
			return err
		}
	}
	if h.writable && !h.detached.Load() {
		if err := h.ws.resolveDeferredContent(ctx, id, wrote); err != nil {
			return err
		}
	}
	return cacheError(closeErr)
}

func cacheError(err error) error {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, cache.ErrDetached), errors.Is(err, cache.ErrCorruptBody), errors.Is(err, vaultfile.ErrTampered):
		return syscall.EIO
	case errors.Is(err, cache.ErrNoBody):
		return syscall.EIO
	}
	return err
}
