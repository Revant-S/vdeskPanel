package workspace

import (
	"fmt"
	"path"
	"strings"

	"vault/internal/index"
	"vault/internal/model"
)

// freeName settles two objects that ended up with one name in one folder: the one placed
// earlier is renamed "name (conflict <device>).ext". Every device that sees the collision
// does the same, so they converge. It returns the name and encrypted name the incoming
// object should use.
func (a *remoteApply) freeName(parentID model.ObjectID, name string, incoming index.Object, version model.Stamp) (string, string, error) {
	existing, taken := a.ws.tree.Lookup(parentID, name)
	if !taken || existing.ID == incoming.ID {
		return name, incoming.EncName, nil
	}
	existingObject, found, err := a.load(existing.ID)
	if err != nil || !found {
		return name, incoming.EncName, err
	}
	if version.After(existingObject.PlacementVer) {
		return name, incoming.EncName, a.renameForConflict(existing, existingObject)
	}
	renamed := a.conflictName(parentID, name, a.commit.DeviceName)
	encName, err := a.ws.Names.EncryptName(incoming.ID, renamed)
	if err != nil {
		return "", "", err
	}
	return renamed, encName, a.logConflictRename(incoming.ID, parentID, name, renamed, false)
}

func (a *remoteApply) renameForConflict(existing model.Entry, object index.Object) error {
	renamed := a.conflictName(existing.ParentID, existing.Name, a.deviceNameOf(object.PlacementVer.Device))
	encName, err := a.ws.Names.EncryptName(existing.ID, renamed)
	if err != nil {
		return err
	}
	if err := a.ws.tree.Move(existing.ID, existing.ParentID, renamed); err != nil {
		return err
	}
	object.EncName = encName
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	a.invalidateEntry(existing.ParentID, existing.Name)
	return a.logConflictRename(existing.ID, existing.ParentID, existing.Name, renamed, existing.IsFolder())
}

func (a *remoteApply) logConflictRename(id, parentID model.ObjectID, from, to string, folder bool) error {
	parentPath, err := a.ws.tree.PathOf(parentID)
	if err != nil {
		return err
	}
	stamp := a.ws.Clock.Next()
	object, found, err := a.load(id)
	if err != nil {
		return err
	}
	if found {
		object.PlacementVer = stamp
		if err := a.tx.PutObject(object); err != nil {
			return err
		}
	}
	op := model.LocalOp{Kind: model.LocalMove, ObjectID: id, Folder: folder, ParentID: parentID, Name: to,
		Path: path.Join(parentPath, to), FromParentID: parentID, FromName: from, FromPath: path.Join(parentPath, from)}
	return appendOp(a.tx, op, stamp, a.ws.Now())
}

func (a *remoteApply) conflictName(parentID model.ObjectID, name, device string) string {
	extension := path.Ext(name)
	base := strings.TrimSuffix(name, extension)
	if base == "" {
		base, extension = name, ""
	}
	candidate := fmt.Sprintf("%s (conflict %s)%s", base, device, extension)
	for n := 2; ; n++ {
		if _, taken := a.ws.tree.Lookup(parentID, candidate); !taken {
			return candidate
		}
		candidate = fmt.Sprintf("%s (conflict %s) %d%s", base, device, n, extension)
	}
}

func (a *remoteApply) deviceNameOf(device model.DeviceID) string {
	if device == a.ws.config.Device {
		return a.ws.config.DeviceName
	}
	if device == a.commit.Device {
		return a.commit.DeviceName
	}
	return string(device)
}

// supersedeLocal keeps a local version that lost a conflict: its body moves to a hidden
// holder object that the next batch uploads straight into the trash. The body moves after
// the transaction, since the cache writes to the index itself; effects run in order, so it
// moves before the losing object's body is freed.
func (a *remoteApply) supersedeLocal(object index.Object, reason model.DeleteReason) error {
	holder, err := a.newHolder(object)
	if err != nil {
		return err
	}
	a.after = append(a.after, func() {
		if err := a.ws.Cache.Replace(a.ctx, holder.ID, a.ws.Cache.BodyPath(object.ID)); err != nil {
			a.ws.Logger.Error("keep superseded body", "oid", object.ID, "holder", holder.ID, "err", err)
		}
	})
	holder.State = model.Dirty
	if err := a.queueSupersede(holder, object, reason); err != nil {
		return err
	}
	return a.dropOps(object.ID, model.LocalModify, model.LocalAttr)
}

// supersedeUploaded moves a version that already reached Drive, and then lost, into the trash.
func (a *remoteApply) supersedeUploaded(object index.Object, reason model.DeleteReason) error {
	holder, err := a.newHolder(object)
	if err != nil {
		return err
	}
	holder.DriveID, holder.BodyID, holder.BodyMD5, holder.ContentMAC = object.DriveID, object.BodyID, object.BodyMD5, object.ContentMAC
	holder.State = model.CloudOnly
	return a.queueSupersede(holder, object, reason)
}

func (a *remoteApply) newHolder(object index.Object) (index.Object, error) {
	id, err := model.NewObjectID(a.ws.Random)
	if err != nil {
		return index.Object{}, err
	}
	name := "superseded"
	if entry, ok := a.ws.tree.Get(object.ID); ok {
		name = entry.Name
	}
	encName, err := a.ws.Names.EncryptName(id, name)
	if err != nil {
		return index.Object{}, err
	}
	stamp := a.ws.Clock.Next()
	return index.Object{ID: id, ParentID: object.ParentID, Kind: model.KindFile, EncName: encName, Size: object.Size,
		ModTime: object.ModTime, Executable: object.Executable, ContentVer: object.ContentVer, PlacementVer: stamp, DeleteVer: stamp}, nil
}

func (a *remoteApply) queueSupersede(holder, object index.Object, reason model.DeleteReason) error {
	if err := a.tx.PutObject(holder); err != nil {
		return err
	}
	objectPath, _ := a.ws.tree.PathOf(object.ID)
	op := model.LocalOp{Kind: model.LocalSupersede, ObjectID: holder.ID, Path: objectPath, Reason: reason}
	return appendOp(a.tx, op, holder.PlacementVer, a.ws.Now())
}
