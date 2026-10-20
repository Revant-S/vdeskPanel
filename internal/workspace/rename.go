package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

func (w *Workspace) Rename(ctx context.Context, parent model.Ino, name string, newParent model.Ino, newName string, noReplace bool) error {
	parentID, err := w.objectOf(parent)
	if err != nil {
		return err
	}
	newParentID, err := w.objectOf(newParent)
	if err != nil {
		return err
	}
	if err := crypto.ValidateName(newName); err != nil {
		return nameError(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	source, ok := w.tree.Lookup(parentID, name)
	if !ok {
		return fs.ErrNotExist
	}
	if w.isDesktopTrashFiles(newParentID) && !source.LocalOnly {
		return w.trashFromDesktop(ctx, source, newParent, newName)
	}
	target, exists := w.tree.Lookup(newParentID, newName)
	switch {
	case exists && target.ID == source.ID:
		return nil
	case exists && noReplace:
		return fs.ErrExist
	case exists && source.IsFolder():
		return w.renameFolderOver(ctx, source, target, newParentID, newName)
	case exists:
		if target.IsFolder() {
			return syscall.EISDIR
		}
		return w.renameFileOver(ctx, source, target)
	}
	return w.moveObject(ctx, source, newParentID, newName)
}

func (w *Workspace) renameFolderOver(ctx context.Context, source, target model.Entry, newParentID model.ObjectID, newName string) error {
	if !target.IsFolder() {
		return syscall.ENOTDIR
	}
	if len(w.tree.Children(target.ID)) > 0 {
		return syscall.ENOTEMPTY
	}
	if err := w.deleteObject(ctx, target, 0); err != nil {
		return err
	}
	return w.moveObject(ctx, source, newParentID, newName)
}

// moveObject changes an object's parent or name. A move keeps the encrypted name, because
// the name is bound to the object ID and not to its parent.
func (w *Workspace) moveObject(ctx context.Context, source model.Entry, newParentID model.ObjectID, newName string) error {
	newParent, ok := w.tree.Get(newParentID)
	if !ok {
		return fs.ErrNotExist
	}
	if newParent.LocalOnly && !source.LocalOnly {
		return w.deleteObject(ctx, source, 0)
	}
	fromPath, err := w.tree.PathOf(source.ID)
	if err != nil {
		return err
	}
	if err := w.tree.Move(source.ID, newParentID, newName); err != nil {
		return err
	}
	if err := w.persistMove(ctx, source, newParentID, newName, fromPath); err != nil {
		w.tree.Move(source.ID, source.ParentID, source.Name)
		return err
	}
	return nil
}

func (w *Workspace) persistMove(ctx context.Context, source model.Entry, newParentID model.ObjectID, newName, fromPath string) error {
	toPath, err := w.tree.PathOf(source.ID)
	if err != nil {
		return err
	}
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(source.ID)
		if err != nil {
			return err
		}
		if newName != source.Name {
			if object.EncName, err = w.Names.EncryptName(source.ID, newName); err != nil {
				return nameError(err)
			}
		}
		object.ParentID, object.PlacementVer = newParentID, stamp
		if err := tx.PutObject(object); err != nil {
			return err
		}
		if source.LocalOnly {
			return nil
		}
		op := model.LocalOp{Kind: model.LocalMove, ObjectID: source.ID, Folder: source.IsFolder(), ParentID: newParentID,
			Name: newName, Path: toPath, FromParentID: source.ParentID, FromName: source.Name, FromPath: fromPath}
		return appendOp(tx, op, stamp, w.quietDeadline())
	})
}

// renameFileOver is the atomic-save pattern: the target keeps its object ID and inode, and
// takes the source's body. The source object is absorbed.
func (w *Workspace) renameFileOver(ctx context.Context, source, target model.Entry) error {
	if err := w.ensureLocalBody(ctx, source.ID); err != nil {
		return err
	}
	w.handles.detach(target.ID)
	if err := w.Cache.Replace(ctx, target.ID, w.Cache.BodyPath(source.ID)); err != nil {
		return fmt.Errorf("replace body: %w", err)
	}
	path, err := w.tree.PathOf(target.ID)
	if err != nil {
		return err
	}
	if err := w.persistRenameOver(ctx, source, target, path); err != nil {
		return err
	}
	w.tree.Remove(target.ID)
	if err := w.tree.Move(source.ID, target.ParentID, target.Name); err != nil {
		return err
	}
	if err := w.tree.Rebind(source.ID, target.ID); err != nil {
		return err
	}
	w.inodes.rebind(source.ID, target.ID)
	w.handles.rebind(source.ID, target.ID)
	return nil
}

func (w *Workspace) persistRenameOver(ctx context.Context, source, target model.Entry, path string) error {
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		sourceObject, err := tx.Object(source.ID)
		if err != nil {
			return err
		}
		targetObject, err := tx.Object(target.ID)
		if err != nil {
			return err
		}
		targetObject.Size, targetObject.ModTime, targetObject.Executable = sourceObject.Size, sourceObject.ModTime, sourceObject.Executable
		targetObject.State, targetObject.ContentVer, targetObject.BodyID = model.Dirty, stamp, nil
		if err := tx.PutObject(targetObject); err != nil {
			return err
		}
		sourceObject.DeleteVer = stamp
		if err := tx.PutObject(sourceObject); err != nil {
			return err
		}
		if err := tx.DeleteSegmentBitmap(target.ID); err != nil {
			return err
		}
		if target.LocalOnly {
			return nil
		}
		modify := model.LocalOp{Kind: model.LocalModify, ObjectID: target.ID, Path: path, ReplacedFrom: source.ID}
		if err := appendOp(tx, modify, stamp, w.quietDeadline()); err != nil {
			return err
		}
		absorb := model.LocalOp{Kind: model.LocalAbsorb, ObjectID: source.ID, ReplacedFrom: target.ID}
		return appendOp(tx, absorb, w.Clock.Next(), w.quietDeadline())
	})
}

// ensureLocalBody downloads a body that is not fully on disk, so it can be moved as a whole.
func (w *Workspace) ensureLocalBody(ctx context.Context, id model.ObjectID) error {
	object, err := w.indexObject(ctx, id)
	if err != nil {
		return err
	}
	if object.State != model.CloudOnly && object.State != model.Partial {
		return nil
	}
	entry, err := w.openCacheEntry(ctx, object, true)
	if err != nil {
		return err
	}
	defer entry.Close()
	return cacheError(entry.EnsureComplete(ctx))
}
