package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"syscall"

	"vault/internal/fusefs"
	"vault/internal/index"
	"vault/internal/model"
)

func (w *Workspace) Mkdir(ctx context.Context, parent model.Ino, name string) (fusefs.Attr, error) {
	parentID, err := w.objectOf(parent)
	if err != nil {
		return fusefs.Attr{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, err := w.createObject(ctx, parentID, name, model.KindFolder, false)
	if err != nil {
		return fusefs.Attr{}, err
	}
	return w.attrOf(entry), nil
}

func (w *Workspace) Create(ctx context.Context, parent model.Ino, name string, executable bool, caller fusefs.Caller) (fusefs.Attr, fusefs.Handle, error) {
	parentID, err := w.objectOf(parent)
	if err != nil {
		return fusefs.Attr{}, nil, err
	}
	w.mu.Lock()
	entry, err := w.createObject(ctx, parentID, name, model.KindFile, executable)
	if err != nil {
		w.mu.Unlock()
		return fusefs.Attr{}, nil, err
	}
	cacheEntry, err := w.Cache.Create(ctx, entry.ID)
	if err != nil {
		w.discardCreated(ctx, entry)
		w.mu.Unlock()
		return fusefs.Attr{}, nil, fmt.Errorf("create body: %w", err)
	}
	w.mu.Unlock()
	h := &handle{ws: w, id: entry.ID, entry: cacheEntry, writable: true, processID: caller.PID}
	h.wrote.Store(true)
	w.handles.add(h)
	return w.attrOf(entry), h, nil
}

// createObject inserts a new object into the tree and the index together. Callers hold w.mu.
func (w *Workspace) createObject(ctx context.Context, parentID model.ObjectID, name string, kind model.Kind, executable bool) (model.Entry, error) {
	parent, ok := w.tree.Get(parentID)
	if !ok {
		return model.Entry{}, fs.ErrNotExist
	}
	if !parent.IsFolder() {
		return model.Entry{}, syscall.ENOTDIR
	}
	if _, exists := w.tree.Lookup(parentID, name); exists {
		return model.Entry{}, fs.ErrExist
	}
	id, err := model.NewObjectID(w.Random)
	if err != nil {
		return model.Entry{}, err
	}
	encName, err := w.Names.EncryptName(id, name)
	if err != nil {
		return model.Entry{}, nameError(err)
	}
	localOnly := w.isLocalOnlyPlace(parent, name)
	entry := model.Entry{ID: id, ParentID: parentID, Name: name, Kind: kind, ModTime: w.Now(),
		State: model.Dirty, Executable: executable, LocalOnly: localOnly}
	if localOnly {
		entry.State = model.Local
	}
	if err := w.tree.Insert(entry); err != nil {
		return model.Entry{}, err
	}
	if err := w.persistCreated(ctx, entry, encName); err != nil {
		w.tree.Remove(id)
		return model.Entry{}, err
	}
	return entry, nil
}

func (w *Workspace) persistCreated(ctx context.Context, entry model.Entry, encName string) error {
	path, err := w.tree.PathOf(entry.ID)
	if err != nil {
		return err
	}
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		object := index.Object{ID: entry.ID, ParentID: entry.ParentID, Kind: entry.Kind, EncName: encName,
			ModTime: entry.ModTime, Executable: entry.Executable, State: entry.State, LocalOnly: entry.LocalOnly,
			ContentVer: stamp, PlacementVer: stamp}
		if err := tx.PutObject(object); err != nil {
			return err
		}
		if entry.LocalOnly {
			return nil
		}
		op := model.LocalOp{Kind: model.LocalCreate, ObjectID: entry.ID, Folder: entry.IsFolder(),
			ParentID: entry.ParentID, Name: entry.Name, Path: path}
		return appendOp(tx, op, stamp, w.quietDeadline())
	})
}

func (w *Workspace) discardCreated(ctx context.Context, entry model.Entry) {
	w.tree.Remove(entry.ID)
	err := w.update(ctx, func(tx *index.Tx) error {
		ops, err := tx.OpsForObject(entry.ID)
		if err != nil {
			return err
		}
		for _, op := range ops {
			if err := tx.DeleteOps([]int64{op.Seq}); err != nil {
				return err
			}
		}
		return tx.DeleteObject(entry.ID)
	})
	if err != nil {
		w.Logger.Error("discard half-created object", "oid", entry.ID, "err", err)
	}
}

func (w *Workspace) isLocalOnlyPlace(parent model.Entry, name string) bool {
	return parent.LocalOnly || (parent.ID == model.RootID && name == w.config.desktopTrashName())
}
