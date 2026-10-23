package workspace

import (
	"vault/internal/journal"
	"vault/internal/model"
)

func (a *remoteApply) trashRestore(op journal.Op) error {
	if op.Trash == nil {
		return nil
	}
	if err := a.tx.DeleteTrash(op.Trash.ID); err != nil {
		return err
	}
	rows, err := a.tx.ObjectsWithTrashID(op.Trash.ID)
	if err != nil || len(rows) == 0 {
		return err
	}
	// A restore into a folder deleted here meanwhile joins that folder's trash entry, as a
	// move or create into it would (row 7).
	parent, parentLive, err := a.parentState(op.ParentID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		row.TrashID, row.DeleteVer, row.PlacementVer = "", model.Stamp{}, op.Version
		if !parentLive {
			row.TrashID, row.DeleteVer = parent.TrashID, parent.DeleteVer
		}
		if row.ID == op.ObjectID {
			row.ParentID, row.EncName = op.ParentID, op.EncName
		}
		if row.Kind == model.KindFile {
			row.State = model.CloudOnly
		}
		if err := a.tx.PutObject(row); err != nil {
			return err
		}
	}
	if !parentLive {
		return nil
	}
	name, err := a.ws.Names.DecryptName(op.ObjectID, op.EncName)
	if err != nil {
		return err
	}
	if top, found, err := a.load(op.ObjectID); err != nil || !found {
		return err
	} else if _, top.EncName, err = a.freeName(op.ParentID, name, top, op.Version); err != nil {
		return err
	} else if err := a.tx.PutObject(top); err != nil {
		return err
	}
	a.invalidateEntry(op.ParentID, name)
	return a.ws.insertSubtree(a.tx, op.ObjectID)
}

func (a *remoteApply) trashPurge(op journal.Op) error {
	if op.Trash == nil {
		return nil
	}
	rows, err := a.tx.ObjectsWithTrashID(op.Trash.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := a.tx.DeleteSegmentBitmap(row.ID); err != nil {
			return err
		}
		if err := a.tx.DeleteObject(row.ID); err != nil {
			return err
		}
	}
	return a.tx.DeleteTrash(op.Trash.ID)
}
