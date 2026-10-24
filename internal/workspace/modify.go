package workspace

import (
	"context"

	"vault/internal/index"
	"vault/internal/model"
)

// noteLocalWrite keeps the in-memory size and time current between writes; the index is
// updated once, when the handle is released.
func (w *Workspace) noteLocalWrite(id model.ObjectID, size int64) {
	entry, ok := w.tree.Get(id)
	if !ok {
		return
	}
	entry.Size, entry.ModTime = size, w.Now()
	if !entry.LocalOnly {
		entry.State = model.Dirty
	}
	w.tree.Update(entry)
}

func (w *Workspace) markDirty(ctx context.Context, id model.ObjectID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.tree.Get(id)
	if !ok || entry.LocalOnly {
		return nil
	}
	if err := w.update(ctx, func(tx *index.Tx) error { return tx.SetLocalState(id, model.Dirty) }); err != nil {
		return err
	}
	entry.State = model.Dirty
	return w.tree.Update(entry)
}

// recordModify writes the final size and time of a changed file and queues its upload.
func (w *Workspace) recordModify(ctx context.Context, id model.ObjectID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.tree.Get(id)
	if !ok {
		return nil
	}
	path, err := w.tree.PathOf(id)
	if err != nil {
		return err
	}
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(id)
		if err != nil {
			return err
		}
		object.Size, object.ModTime, object.Executable = entry.Size, entry.ModTime, entry.Executable
		if entry.LocalOnly {
			return tx.PutObject(object)
		}
		object.State, object.ContentVer = model.Dirty, stamp
		if err := tx.PutObject(object); err != nil {
			return err
		}
		return appendOp(tx, model.LocalOp{Kind: model.LocalModify, ObjectID: id, Path: path}, stamp, w.quietDeadline())
	})
}

func (w *Workspace) HasOpenWriter(id model.ObjectID) bool {
	return w.handles.hasWriter(id)
}

// recoverInterruptedWrites covers a crash while a file was open for writing: the body on disk
// is newer than the index row, and its modify op may never have been queued.
func (w *Workspace) recoverInterruptedWrites(ctx context.Context) error {
	var dirty []index.Object
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		var err error
		dirty, err = tx.ObjectsInState(model.Dirty)
		return err
	})
	if err != nil {
		return err
	}
	for _, object := range dirty {
		if object.Kind == model.KindFile && object.TrashID != "" {
			if err := w.settleTrashedDirty(ctx, object); err != nil {
				return err
			}
			continue
		}
		if object.Kind == model.KindFolder || object.LocalOnly || !isLive(object) {
			continue
		}
		if err := w.recoverDirtyFile(ctx, object); err != nil {
			return err
		}
	}
	return nil
}

// settleTrashedDirty repairs trash entries that older builds left Dirty after uploading them,
// which kept them in the not-backed-up count forever and so held back every restore.
func (w *Workspace) settleTrashedDirty(ctx context.Context, object index.Object) error {
	return w.Index.Update(ctx, func(tx *index.Tx) error {
		ops, err := tx.OpsForObject(object.ID)
		if err != nil || len(ops) > 0 || object.DriveID == "" {
			return err
		}
		object.State = model.CloudOnly
		return tx.PutObject(object)
	})
}

func (w *Workspace) recoverDirtyFile(ctx context.Context, object index.Object) error {
	// A failed repair must never be what keeps the vault from opening; the body stays as it
	// was, and reads report any damage as before.
	if repaired, err := w.Cache.RecoverInterruptedRewrite(object.ID); err != nil {
		w.Logger.Warn("repair a body whose rewrite a crash cut short", "oid", object.ID, "err", err)
	} else if repaired {
		w.Logger.Warn("repaired a body whose rewrite a crash cut short", "oid", object.ID)
	}
	size, err := w.bodySize(ctx, object)
	if err != nil {
		return err
	}
	if entry, ok := w.tree.Get(object.ID); ok && entry.Size != size {
		entry.Size = size
		w.tree.Update(entry)
	}
	var queued bool
	if err := w.Index.View(ctx, func(tx *index.Tx) error {
		ops, err := tx.OpsForObject(object.ID)
		queued = len(ops) > 0
		return err
	}); err != nil {
		return err
	}
	if queued && size == object.Size {
		return nil
	}
	path, _ := w.tree.PathOf(object.ID)
	return w.update(ctx, func(tx *index.Tx) error {
		object.Size = size
		if queued {
			return tx.PutObject(object)
		}
		stamp := w.Clock.Next()
		object.ContentVer = stamp
		if err := tx.PutObject(object); err != nil {
			return err
		}
		return appendOp(tx, model.LocalOp{Kind: model.LocalModify, ObjectID: object.ID, Path: path}, stamp, w.Now())
	})
}

func (w *Workspace) bodySize(ctx context.Context, object index.Object) (int64, error) {
	entry, err := w.openCacheEntry(ctx, object, false)
	if err != nil {
		return 0, err
	}
	defer entry.Close()
	return entry.Size(), nil
}
