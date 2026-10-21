package workspace

import (
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
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
	for _, row := range rows {
		row.TrashID, row.DeleteVer = "", model.Stamp{}
		if row.ID == op.ObjectID {
			row.ParentID, row.EncName, row.PlacementVer = op.ParentID, op.EncName, op.Version
		}
		if row.Kind == model.KindFile {
			row.State = model.CloudOnly
		}
		if err := a.tx.PutObject(row); err != nil {
			return err
		}
	}
	if _, parentLive, err := a.parentState(op.ParentID); err != nil || !parentLive {
		return err
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
