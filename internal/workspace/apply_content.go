package workspace

import (
	"encoding/json"
	"time"

	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
)

// content applies a remote content change, deciding conflicts by the content register:
// the newer version wins, and the losing version is kept in the trash by its author.
func (a *remoteApply) content(op journal.Op) error {
	object, found, err := a.load(op.ObjectID)
	if err != nil {
		return err
	}
	if !found {
		if op.EncName == "" {
			return nil
		}
		return a.create(op)
	}
	if !isLive(object) {
		return a.contentForDeleted(object, op)
	}
	if a.ws.handles.hasWriter(object.ID) {
		return a.deferContent(object, op)
	}
	if op.DriveID == object.DriveID {
		return a.metadata(object, op)
	}
	unsynced, err := a.hasUnsyncedContent(object)
	if err != nil {
		return err
	}
	newer := op.Version.After(object.ContentVer)
	concurrent := object.DriveID != "" && op.PreviousDriveID != object.DriveID
	switch {
	case unsynced && newer:
		if err := a.supersedeLocal(object, model.SupersededBy(a.commit.DeviceName)); err != nil {
			return err
		}
	case unsynced:
		return nil
	case !newer:
		return nil
	case concurrent && object.ContentVer.Device == a.ws.config.Device:
		if err := a.supersedeUploaded(object, model.SupersededBy(a.commit.DeviceName)); err != nil {
			return err
		}
	}
	return a.takeRemoteContent(&object, op)
}

func (a *remoteApply) metadata(object index.Object, op journal.Op) error {
	if !op.Version.After(object.ContentVer) {
		return nil
	}
	object.ModTime, object.Executable, object.ContentVer = op.ModTime, op.Executable, op.Version
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	return a.updateTreeEntry(object)
}

func (a *remoteApply) takeRemoteContent(object *index.Object, op journal.Op) error {
	wasLocal := object.State == model.Local
	object.DriveID, object.BodyID, object.BodyMD5, object.ContentMAC = op.DriveID, op.BodyID, op.BodyMD5, op.ContentMAC
	object.Size, object.ModTime, object.Executable, object.ContentVer = op.Size, op.ModTime, op.Executable, op.Version
	object.State = model.CloudOnly
	if err := a.tx.PutObject(*object); err != nil {
		return err
	}
	if err := a.tx.DeleteSegmentBitmap(object.ID); err != nil {
		return err
	}
	id := object.ID
	a.dropLocalBody(id)
	if wasLocal && a.ws.WantLocal != nil {
		a.after = append(a.after, func() { a.ws.WantLocal(id) })
	}
	return a.updateTreeEntry(*object)
}

func (a *remoteApply) updateTreeEntry(object index.Object) error {
	entry, ok := a.ws.tree.Get(object.ID)
	if !ok {
		return nil
	}
	entry.Size, entry.ModTime, entry.Executable, entry.State = object.Size, object.ModTime, object.Executable, object.State
	return a.ws.tree.Update(entry)
}

// contentForDeleted handles a remote edit of something deleted here. A newer edit brings the
// file back; an older one leaves it deleted, but a pending local delete is pointed at the
// newer Drive version so that version is what ends up in the trash.
func (a *remoteApply) contentForDeleted(object index.Object, op journal.Op) error {
	if _, parentLive, err := a.parentState(object.ParentID); err != nil || !parentLive {
		if err != nil {
			return err
		}
		return a.contentInsideDeletedFolder(object, op)
	}
	pendingDelete := object.TrashID == ""
	if !op.Version.After(object.DeleteVer) {
		if !pendingDelete {
			return nil
		}
		object.DriveID, object.BodyID, object.BodyMD5, object.ContentMAC = op.DriveID, op.BodyID, op.BodyMD5, op.ContentMAC
		return a.tx.PutObject(object)
	}
	if err := a.dropOps(object.ID, model.LocalDelete); err != nil {
		return err
	}
	object.DeleteVer, object.TrashID = model.Stamp{}, ""
	if err := a.takeRemoteContent(&object, op); err != nil {
		return err
	}
	return a.reinsert(object)
}

// contentInsideDeletedFolder handles an edit of a file whose folder is deleted here. The
// folder delete wins whatever the stamps, so the file stays deleted. The edit's body was
// uploaded into the deleted folder and its author removes the version it replaced, so the
// trash entry takes the newer version, as every other device does.
func (a *remoteApply) contentInsideDeletedFolder(object index.Object, op journal.Op) error {
	if !op.Version.After(object.ContentVer) {
		return nil
	}
	object.DriveID, object.BodyID, object.BodyMD5, object.ContentMAC = op.DriveID, op.BodyID, op.BodyMD5, op.ContentMAC
	object.Size, object.ModTime, object.Executable, object.ContentVer = op.Size, op.ModTime, op.Executable, op.Version
	return a.tx.PutObject(object)
}

func (a *remoteApply) reinsert(object index.Object) error {
	if _, inTree := a.ws.tree.Get(object.ID); inTree {
		return nil
	}
	if _, parentLive, err := a.parentState(object.ParentID); err != nil || !parentLive {
		return err
	}
	name, err := a.ws.Names.DecryptName(object.ID, object.EncName)
	if err != nil {
		return err
	}
	if name, object.EncName, err = a.freeName(object.ParentID, name, object, object.PlacementVer); err != nil {
		return err
	}
	if err := a.tx.PutObject(object); err != nil {
		return err
	}
	a.invalidateEntry(object.ParentID, name)
	return a.ws.tree.Insert(entryOfObject(object, name))
}

// deferContent parks a remote edit while a program has the file open for writing; it is
// resolved when the last writer closes.
func (a *remoteApply) deferContent(object index.Object, op journal.Op) error {
	deferred, err := json.Marshal(deferredContent{Op: op, DeviceName: a.commit.DeviceName, Device: a.commit.Device})
	if err != nil {
		return err
	}
	object.DeferredContent = deferred
	return a.tx.PutObject(object)
}

type deferredContent struct {
	Op         journal.Op     `json:"op"`
	Device     model.DeviceID `json:"device"`
	DeviceName string         `json:"deviceName"`
}

func wallTime(stamp model.Stamp) time.Time {
	return time.UnixMilli(stamp.WallMillis)
}
