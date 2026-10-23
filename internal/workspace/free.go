package workspace

import (
	"context"
	"errors"

	"vault/internal/cache"
	"vault/internal/index"
	"vault/internal/model"
)

var ErrNotBackedUp = errors.New("workspace: changes are not backed up yet")

// FreeLocal drops the local bodies of a file, or of every file under a folder, leaving them
// cloud only. Files with changes not yet on Drive, or open for writing, are refused.
func (w *Workspace) FreeLocal(ctx context.Context, id model.ObjectID) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var freed int64
	for _, entry := range w.filesUnder(id) {
		// The state alone can't tell: reads leave partial bodies on CloudOnly files.
		if entry.State == model.CloudOnly && !w.Cache.HasBody(entry.ID) {
			continue
		}
		if entry.State == model.Dirty || entry.State == model.Corrupt || w.handles.hasWriter(entry.ID) {
			return freed, ErrNotBackedUp
		}
		if err := w.Cache.Free(ctx, entry.ID); err != nil && !errors.Is(err, cache.ErrNoBody) {
			return freed, err
		}
		if err := w.update(ctx, func(tx *index.Tx) error { return tx.SetLocalState(entry.ID, model.CloudOnly) }); err != nil {
			return freed, err
		}
		entry.State = model.CloudOnly
		w.tree.Update(entry)
		freed += entry.Size
	}
	return freed, nil
}

// NoteLocal records that the restorer finished a body, so the tree, and with it Free and the
// tree view, see the file as local.
func (w *Workspace) NoteLocal(id model.ObjectID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if entry, ok := w.tree.Get(id); ok && (entry.State == model.CloudOnly || entry.State == model.Partial) {
		entry.State = model.Local
		w.tree.Update(entry)
	}
}

func (w *Workspace) filesUnder(id model.ObjectID) []model.Entry {
	entry, ok := w.tree.Get(id)
	if !ok {
		return nil
	}
	if !entry.IsFolder() {
		return []model.Entry{entry}
	}
	var files []model.Entry
	for _, child := range w.tree.Children(id) {
		files = append(files, w.filesUnder(child.ID)...)
	}
	return files
}

// FilesUnder lists the files at or below an object, for actions that apply to a folder.
func (w *Workspace) FilesUnder(id model.ObjectID) []model.Entry {
	return w.filesUnder(id)
}
