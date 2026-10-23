package syncer

import (
	"context"
	"errors"
	"os"
	"slices"

	"vault/internal/cache"
	"vault/internal/index"
	"vault/internal/model"
)

// finalizeBatch runs after the commit: it deletes superseded Drive versions, settles local
// bodies, and only then records the results and drops the batch. Every step tolerates being
// repeated after a crash.
func (o *Outgoing) finalizeBatch(ctx context.Context, plan *BatchPlan) error {
	for _, op := range plan.Ops {
		if err := o.deleteSupersededRemote(ctx, op); err != nil {
			return err
		}
	}
	for _, op := range plan.Ops {
		o.settleLocalBody(ctx, plan, op)
	}
	defer o.announceMovedOut(plan)
	return o.Index.Update(ctx, func(tx *index.Tx) error {
		trashIDs := map[model.ObjectID]string{}
		for _, op := range plan.Ops {
			if op.Kind == NetDelete && op.SubsumedBy == "" {
				trashIDs[op.ObjectID] = op.TrashID
			}
		}
		for _, op := range plan.Ops {
			if err := o.recordResult(tx, plan, op, trashIDs); err != nil {
				return err
			}
		}
		if err := tx.DeleteOpsOfBatch(plan.ID); err != nil {
			return err
		}
		return tx.DeleteBatch(plan.ID)
	})
}

func (o *Outgoing) deleteSupersededRemote(ctx context.Context, op BatchOp) error {
	old := op.Object.DriveID
	switch {
	case op.Skipped || old == "":
		return nil
	case op.Kind == NetModify && op.Result.DriveID != old,
		op.Kind == NetDelete && op.TrashLatest && op.Result.DriveID != "" && op.Result.DriveID != old,
		op.Kind == NetAbsorb:
		return o.deleteUnlessTrashed(ctx, old)
	}
	return nil
}

// deleteUnlessTrashed removes a superseded version, unless another device has meanwhile
// moved it into the trash, where it is now the body of a trash entry.
func (o *Outgoing) deleteUnlessTrashed(ctx context.Context, driveID string) error {
	current, err := o.Remote.Get(ctx, driveID)
	if errors.Is(err, ErrRemoteNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if slices.Contains(current.Parents, o.config.TrashFolderID) {
		return nil
	}
	return ignoreNotFound(o.Remote.Delete(ctx, driveID))
}

// settleLocalBody makes the uploaded snapshot the cache body when nothing changed since, so
// the cache matches Drive and disk use returns to one copy.
func (o *Outgoing) settleLocalBody(ctx context.Context, plan *BatchPlan, op BatchOp) {
	if op.Snapshot != nil {
		uploadedIsCurrent := (op.Kind == NetCreate || op.Kind == NetModify) && !op.Result.Copied &&
			!o.Workspace.HasOpenWriter(op.ObjectID) && !o.hasLaterOps(ctx, plan.ID, op.ObjectID)
		if uploadedIsCurrent {
			if err := o.Cache.Replace(ctx, op.ObjectID, op.Snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				o.Logger.Warn("swap uploaded snapshot into cache", "oid", op.ObjectID, "err", err)
			}
		}
		o.Cache.RemoveSnapshot(plan.ID, op.ObjectID)
	}
	if op.Kind == NetDelete || op.Kind == NetDropLocal || op.Kind == NetAbsorb {
		if err := o.Cache.Free(ctx, op.ObjectID); err != nil && !errors.Is(err, cache.ErrNoBody) && !errors.Is(err, os.ErrNotExist) {
			o.Logger.Warn("free deleted body", "oid", op.ObjectID, "err", err)
		}
	}
}

func (o *Outgoing) hasLaterOps(ctx context.Context, batchID string, id model.ObjectID) bool {
	later := false
	o.Index.View(ctx, func(tx *index.Tx) error {
		ops, err := tx.OpsForObject(id)
		for _, op := range ops {
			later = later || op.BatchID != batchID
		}
		return err
	})
	return later
}

func (o *Outgoing) recordResult(tx *index.Tx, plan *BatchPlan, op BatchOp, trashIDs map[model.ObjectID]string) error {
	if op.Kind == NetDropLocal || op.Kind == NetAbsorb {
		if err := tx.DeleteSegmentBitmap(op.ObjectID); err != nil {
			return err
		}
		return tx.DeleteObject(op.ObjectID)
	}
	object, err := tx.Object(op.ObjectID)
	if errors.Is(err, index.ErrNotFound) || op.Skipped {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case op.Kind == NetCreate && op.Folder:
		object.DriveID = op.Result.DriveID
	case op.Kind == NetCreate, op.Kind == NetModify:
		applyContentResult(&object, op.Result)
	case op.Kind == NetDelete:
		return o.recordTrash(tx, op, object, trashIDs)
	}
	if object.State == model.Dirty && o.isClean(tx, plan.ID, op.ObjectID) {
		object.State = model.Local
	}
	return tx.PutObject(object)
}

func applyContentResult(object *index.Object, result ContentResult) {
	object.DriveID, object.BodyID, object.BodyMD5, object.ContentMAC = result.DriveID, result.BodyID, result.BodyMD5, result.ContentMAC
}

func (o *Outgoing) recordTrash(tx *index.Tx, op BatchOp, object index.Object, trashIDs map[model.ObjectID]string) error {
	if op.SubsumedBy != "" {
		object.TrashID = trashIDs[op.SubsumedBy]
		return tx.PutObject(object)
	}
	if op.TrashLatest {
		applyContentResult(&object, op.Result)
	}
	object.TrashID = op.TrashID
	if err := tx.PutObject(object); err != nil {
		return err
	}
	return tx.PutTrash(index.TrashRecord{ID: op.TrashID, ObjectID: op.ObjectID, OriginalPath: op.Path, DeletedAt: o.Now(),
		Device: o.config.Device, Reason: string(op.Reason), Size: object.Size, FileCount: max(op.FileCount, boolCount(!op.Folder)),
		Folder: op.Folder, ContentMAC: object.ContentMAC, DriveID: object.DriveID})
}

func (o *Outgoing) isClean(tx *index.Tx, batchID string, id model.ObjectID) bool {
	ops, err := tx.OpsForObject(id)
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.BatchID != batchID {
			return false
		}
	}
	return !o.Workspace.HasOpenWriter(id)
}

func (o *Outgoing) announceMovedOut(plan *BatchPlan) {
	if o.OnMovedOut == nil {
		return
	}
	for _, op := range plan.Ops {
		if op.Kind == NetDelete && op.SubsumedBy == "" && op.Reason == model.ReasonMovedOut && !op.Skipped {
			o.OnMovedOut(op.Path, op.Folder, max(op.FileCount, 1))
		}
	}
}
