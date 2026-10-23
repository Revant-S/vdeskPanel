package workspace

import (
	"context"

	"github.com/cosmoboy/vault/internal/model"
)

// Nautilus "Move to Trash" renames into .Trash-<uid>/files/ at the mount root. The panel's
// trash is the real one, so such a rename deletes from the vault at once, along with the
// .trashinfo record Nautilus wrote first.

func (w *Workspace) isDesktopTrashFiles(id model.ObjectID) bool {
	entry, ok := w.tree.Get(id)
	if !ok || entry.Name != "files" {
		return false
	}
	trashRoot, ok := w.tree.Get(entry.ParentID)
	return ok && trashRoot.ParentID == model.RootID && trashRoot.Name == w.config.desktopTrashName()
}

func (w *Workspace) trashFromDesktop(ctx context.Context, source model.Entry, filesIno model.Ino, trashedName string) error {
	if err := w.deleteSubtree(ctx, source, 0); err != nil {
		return err
	}
	w.queueEntryInvalidation(filesIno, trashedName)
	trashRoot, ok := w.tree.Lookup(model.RootID, w.config.desktopTrashName())
	if !ok {
		return nil
	}
	info, ok := w.tree.Lookup(trashRoot.ID, "info")
	if !ok {
		return nil
	}
	record, ok := w.tree.Lookup(info.ID, trashedName+".trashinfo")
	if !ok {
		return nil
	}
	if ino, known := w.inodes.existingInode(info.ID); known {
		w.queueEntryInvalidation(ino, record.Name)
	}
	return w.deleteLocalOnly(ctx, record)
}
