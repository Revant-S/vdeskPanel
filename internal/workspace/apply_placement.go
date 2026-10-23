package workspace

import (
	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
)

// placement applies a remote rename or move when it is newer than this device's placement.
// A delete here wins over any move, and a move whose target is gone hides the object with it.
func (a *remoteApply) placement(op journal.Op) error {
	object, found, err := a.load(op.ObjectID)
	if err != nil || !found || !op.Version.After(object.PlacementVer) {
		return err
	}
	object.ParentID, object.EncName, object.PlacementVer = op.ParentID, op.EncName, op.Version
	if !isLive(object) {
		return a.tx.PutObject(object)
	}
	if err := a.dropOps(object.ID, model.LocalMove); err != nil {
		return err
	}
	entry, inTree := a.ws.tree.Get(object.ID)
	parent, parentLive, err := a.parentState(op.ParentID)
	if err != nil {
		return err
	}
	if !parentLive {
		return a.hideUnder(object, parent, entry, inTree)
	}
	name, err := a.ws.Names.DecryptName(object.ID, op.EncName)
	if err != nil {
		return err
	}
	if name, object.EncName, err = a.freeName(op.ParentID, name, object, op.Version); err != nil {
		return err
	}
	if inTree {
		if err := a.ws.tree.Move(object.ID, op.ParentID, name); err != nil {
			a.ws.Logger.Warn("remote move refused locally", "oid", object.ID, "err", err)
			return nil
		}
		a.invalidateEntry(entry.ParentID, entry.Name)
	}
	a.invalidateEntry(op.ParentID, name)
	return a.tx.PutObject(object)
}

func (a *remoteApply) hideUnder(object, parent index.Object, entry model.Entry, inTree bool) error {
	object.DeleteVer, object.TrashID = parent.DeleteVer, parent.TrashID
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	if inTree {
		a.ws.tree.Remove(object.ID)
		a.invalidateEntry(entry.ParentID, entry.Name)
	}
	return nil
}
