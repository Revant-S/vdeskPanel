package workspace

import (
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

// delete applies a remote delete. A newer local edit of a file keeps it alive; otherwise the
// object, and with a folder its whole subtree, leaves the tree and joins the trash entry.
// Local edits that never reached Drive are kept in the trash rather than lost.
func (a *remoteApply) delete(op journal.Op) error {
	if err := a.recordTrash(op); err != nil {
		return err
	}
	object, found, err := a.load(op.ObjectID)
	if err != nil || !found {
		return err
	}
	if !op.Folder && isLive(object) {
		keep, err := a.keepNewerEdit(object, op)
		if err != nil || keep {
			return err
		}
	}
	victims := []index.Object{object}
	if op.Folder {
		if victims, err = a.subtreeObjects(object); err != nil {
			return err
		}
	}
	reason := model.ParentDeletedOn(a.commit.DeviceName)
	if !op.Folder {
		reason = model.SupersededBy(a.commit.DeviceName)
	}
	for _, victim := range victims {
		if err := a.trashLocally(victim, op, reason); err != nil {
			return err
		}
	}
	return nil
}

// keepNewerEdit implements "newer modify beats older delete": the file stays. If the edit is
// not on Drive yet, the delete took its base version to the trash, so the next upload must
// be a fresh file rather than a replacement.
func (a *remoteApply) keepNewerEdit(object index.Object, op journal.Op) (bool, error) {
	unsynced, err := a.hasUnsyncedContent(object)
	if err != nil {
		return false, err
	}
	if object.ContentVer.After(op.Version) {
		if unsynced && op.Trash.DriveID == object.DriveID {
			object.DriveID = ""
			return true, a.tx.PutObject(object)
		}
		return true, nil
	}
	concurrent := object.DriveID != "" && op.Trash.DriveID != object.DriveID
	if !unsynced && concurrent && object.ContentVer.Device == a.ws.config.Device {
		return false, a.supersedeUploaded(object, model.SupersededBy(a.commit.DeviceName))
	}
	return false, nil
}

func (a *remoteApply) trashLocally(object index.Object, op journal.Op, reason model.DeleteReason) error {
	if object.Kind == model.KindFile && isLive(object) {
		unsynced, err := a.hasUnsyncedContent(object)
		if err != nil {
			return err
		}
		if unsynced {
			if err := a.supersedeLocal(object, reason); err != nil {
				return err
			}
		}
	}
	if err := a.dropOps(object.ID, model.LocalCreate, model.LocalModify, model.LocalAttr, model.LocalMove, model.LocalDelete); err != nil {
		return err
	}
	object.DeleteVer, object.TrashID = op.Version, op.Trash.ID
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	return a.removeFromView(object.ID)
}

func (a *remoteApply) removeFromView(id model.ObjectID) error {
	entry, inTree := a.ws.tree.Get(id)
	if !inTree {
		return nil
	}
	if entry.ParentID != "" {
		a.invalidateEntry(entry.ParentID, entry.Name)
	}
	a.ws.tree.Remove(id)
	a.ws.handles.detach(id)
	if !entry.IsFolder() {
		a.dropLocalBody(id)
	}
	return nil
}

func (a *remoteApply) subtreeObjects(folder index.Object) ([]index.Object, error) {
	out := []index.Object{folder}
	for i := 0; i < len(out); i++ {
		children, err := a.tx.ChildrenOf(out[i].ID)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.TrashID == "" {
				out = append(out, child)
			}
		}
	}
	return out, nil
}

// removal applies the remote end of a rename over a file: the replaced object disappears
// without a trash entry, since its content lives on in the target.
func (a *remoteApply) removal(op journal.Op) error {
	object, found, err := a.load(op.ObjectID)
	if err != nil || !found {
		return err
	}
	if unsynced, err := a.hasUnsyncedContent(object); err != nil {
		return err
	} else if unsynced && isLive(object) {
		if err := a.supersedeLocal(object, model.SupersededBy(a.commit.DeviceName)); err != nil {
			return err
		}
	}
	if err := a.removeFromView(object.ID); err != nil {
		return err
	}
	if err := a.dropOps(object.ID, model.LocalCreate, model.LocalModify, model.LocalAttr, model.LocalMove, model.LocalDelete); err != nil {
		return err
	}
	if err := a.tx.DeleteSegmentBitmap(object.ID); err != nil {
		return err
	}
	return a.tx.DeleteObject(object.ID)
}
