package workspace

import (
	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
)

func (a *remoteApply) create(op journal.Op) error {
	if _, found, err := a.load(op.ObjectID); err != nil || found {
		if err != nil {
			return err
		}
		if err := a.content(op); err != nil {
			return err
		}
		return a.placement(op)
	}
	name, err := a.ws.Names.DecryptName(op.ObjectID, op.EncName)
	if err != nil {
		return err
	}
	object := objectFromRemote(op)
	parent, parentLive, err := a.parentState(op.ParentID)
	if err != nil {
		return err
	}
	if !parentLive {
		object.DeleteVer, object.TrashID = parent.DeleteVer, parent.TrashID
		if object.DeleteVer.IsZero() {
			object.DeleteVer = op.Version
		}
		return a.tx.PutObject(object)
	}
	if name, object.EncName, err = a.freeName(op.ParentID, name, object, op.Version); err != nil {
		return err
	}
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	return a.ws.tree.Insert(entryOfObject(object, name))
}

func objectFromRemote(op journal.Op) index.Object {
	object := index.Object{ID: op.ObjectID, ParentID: op.ParentID, Kind: model.KindFile, EncName: op.EncName,
		DriveID: op.DriveID, Size: op.Size, ModTime: op.ModTime, Executable: op.Executable, ContentMAC: op.ContentMAC,
		BodyID: op.BodyID, BodyMD5: op.BodyMD5, ContentVer: op.Version, PlacementVer: op.Version, State: model.CloudOnly}
	if op.Folder {
		object.Kind, object.State = model.KindFolder, model.Local
	}
	return object
}

func entryOfObject(object index.Object, name string) model.Entry {
	return model.Entry{ID: object.ID, ParentID: object.ParentID, Name: name, Kind: object.Kind, Size: object.Size,
		ModTime: object.ModTime, State: object.State, Executable: object.Executable, LocalOnly: object.LocalOnly}
}

func (a *remoteApply) parentState(parentID model.ObjectID) (index.Object, bool, error) {
	if parentID == model.RootID {
		return index.Object{}, true, nil
	}
	parent, found, err := a.load(parentID)
	return parent, found && isLive(parent), err
}
