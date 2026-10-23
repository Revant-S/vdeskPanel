package workspace

import (
	"context"

	"vault/internal/fusefs"
	"vault/internal/index"
	"vault/internal/model"
)

func (w *Workspace) Setattr(ctx context.Context, ino model.Ino, change fusefs.AttrChange) (fusefs.Attr, error) {
	if change.Size != nil {
		if err := w.truncateFile(ctx, ino, *change.Size); err != nil {
			return fusefs.Attr{}, err
		}
	}
	if change.ModTime != nil || change.Executable != nil {
		if err := w.changeMetadata(ctx, ino, change); err != nil {
			return fusefs.Attr{}, err
		}
	}
	return w.Getattr(ctx, ino)
}

func (w *Workspace) truncateFile(ctx context.Context, ino model.Ino, size int64) error {
	opened, _, err := w.Open(ctx, ino, true, false, fusefs.Caller{})
	if err != nil {
		return err
	}
	h := opened.(*handle)
	truncateErr := h.truncate(ctx, size)
	releaseErr := h.Release(ctx)
	if truncateErr != nil {
		return truncateErr
	}
	return releaseErr
}

// changeMetadata applies chmod and utimens. Only the executable bit is kept.
func (w *Workspace) changeMetadata(ctx context.Context, ino model.Ino, change fusefs.AttrChange) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, err := w.entryOf(ino)
	if err != nil {
		return err
	}
	if change.ModTime != nil {
		entry.ModTime = *change.ModTime
	}
	if change.Executable != nil && !entry.IsFolder() {
		entry.Executable = *change.Executable
	}
	path, err := w.tree.PathOf(entry.ID)
	if err != nil {
		return err
	}
	stamp := w.Clock.Next()
	err = w.update(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(entry.ID)
		if err != nil {
			return err
		}
		object.ModTime, object.Executable = entry.ModTime, entry.Executable
		if entry.LocalOnly {
			return tx.PutObject(object)
		}
		object.ContentVer = stamp
		if err := tx.PutObject(object); err != nil {
			return err
		}
		return appendOp(tx, model.LocalOp{Kind: model.LocalAttr, ObjectID: entry.ID, Folder: entry.IsFolder(), Path: path}, stamp, w.quietDeadline())
	})
	if err != nil {
		return err
	}
	return w.tree.Update(entry)
}
