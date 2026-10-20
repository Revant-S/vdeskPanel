package workspace

import (
	"context"
	"io/fs"
	"syscall"

	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

func (w *Workspace) Unlink(ctx context.Context, parent model.Ino, name string, caller fusefs.Caller) error {
	return w.removeChild(ctx, parent, name, caller, false)
}

func (w *Workspace) Rmdir(ctx context.Context, parent model.Ino, name string, caller fusefs.Caller) error {
	return w.removeChild(ctx, parent, name, caller, true)
}

func (w *Workspace) removeChild(ctx context.Context, parent model.Ino, name string, caller fusefs.Caller, folder bool) error {
	parentID, err := w.objectOf(parent)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.tree.Lookup(parentID, name)
	switch {
	case !ok:
		return fs.ErrNotExist
	case folder && !entry.IsFolder():
		return syscall.ENOTDIR
	case !folder && entry.IsFolder():
		return syscall.EISDIR
	case folder && len(w.tree.Children(entry.ID)) > 0:
		return syscall.ENOTEMPTY
	}
	return w.deleteObject(ctx, entry, caller.PID)
}

// deleteObject removes an object from view. Its row and body stay until the delete is synced,
// because the trash entry is built from them. Callers hold w.mu.
func (w *Workspace) deleteObject(ctx context.Context, entry model.Entry, processID uint32) error {
	path, err := w.tree.PathOf(entry.ID)
	if err != nil {
		return err
	}
	if entry.LocalOnly {
		return w.deleteLocalOnly(ctx, entry)
	}
	reason := model.ReasonDeleted
	if w.Classify != nil && processID != 0 && !entry.IsFolder() {
		reason = w.Classify.ClassifyDelete(entry.ID, processID)
	}
	stamp := w.Clock.Next()
	err = w.update(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(entry.ID)
		if err != nil {
			return err
		}
		object.DeleteVer = stamp
		if err := tx.PutObject(object); err != nil {
			return err
		}
		op := model.LocalOp{Kind: model.LocalDelete, ObjectID: entry.ID, Folder: entry.IsFolder(),
			ParentID: entry.ParentID, Name: entry.Name, Path: path, Reason: reason}
		return appendOp(tx, op, stamp, w.Now().Add(w.config.DeleteHold))
	})
	if err != nil {
		return err
	}
	w.forgetEntry(entry.ID)
	return nil
}

func (w *Workspace) deleteLocalOnly(ctx context.Context, entry model.Entry) error {
	err := w.update(ctx, func(tx *index.Tx) error {
		if err := tx.DeleteSegmentBitmap(entry.ID); err != nil {
			return err
		}
		return tx.DeleteObject(entry.ID)
	})
	if err != nil {
		return err
	}
	w.forgetEntry(entry.ID)
	if !entry.IsFolder() {
		if err := w.Cache.Free(ctx, entry.ID); err != nil {
			w.Logger.Warn("free local-only body", "oid", entry.ID, "err", err)
		}
	}
	return nil
}

func (w *Workspace) forgetEntry(id model.ObjectID) {
	w.tree.Remove(id)
	w.handles.detach(id)
	w.inodes.forget(id)
}
