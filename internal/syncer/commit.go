package syncer

import (
	"context"
	"fmt"
	"path"

	"vault/internal/journal"
	"vault/internal/model"
)

// commitBatch records the batch's net effect as one log commit. It runs only after every body
// the commit names is on Drive and verified.
func (o *Outgoing) commitBatch(ctx context.Context, plan *BatchPlan) error {
	ops := journalOps(plan)
	if len(ops) > 0 {
		if _, err := o.Journal.Record(ctx, plan.CommitID, plan.CommitDriveID, ops); err != nil {
			return fmt.Errorf("record commit: %w", err)
		}
	}
	plan.Committed = true
	return o.saveBatch(ctx, plan)
}

func journalOps(plan *BatchPlan) []journal.Op {
	var out []journal.Op
	for _, op := range plan.Ops {
		if op.Skipped {
			continue
		}
		switch op.Kind {
		case NetCreate:
			out = append(out, contentOp(journal.OpCreate, op))
		case NetModify:
			if op.Moved {
				out = append(out, placementOp(op))
			}
			out = append(out, contentOp(journal.OpModify, op))
		case NetMove:
			out = append(out, placementOp(op))
		case NetAttr:
			out = append(out, attrOp(op))
		case NetDelete:
			if op.SubsumedBy == "" {
				out = append(out, deleteOp(op))
			}
		case NetRestore:
			out = append(out, journal.Op{Kind: journal.OpTrashRestore, ObjectID: op.ObjectID, ParentID: op.Object.ParentID,
				EncName: op.Object.EncName, Path: op.Path, Folder: op.Folder, Trash: &journal.TrashRef{ID: op.TrashItem}, Version: op.Version})
		case NetPurge:
			out = append(out, journal.Op{Kind: journal.OpTrashPurge, ObjectID: op.ObjectID, Path: op.Path, Folder: op.Folder,
				Trash: &journal.TrashRef{ID: op.TrashItem}, Version: op.Version})
		case NetAbsorb:
			out = append(out, journal.Op{Kind: journal.OpDelete, ObjectID: op.ObjectID, Path: op.Path,
				Reason: "replaced", Version: op.Version})
		}
	}
	return out
}

func contentOp(kind journal.OpKind, op BatchOp) journal.Op {
	result := op.Result
	return journal.Op{Kind: kind, ObjectID: op.ObjectID, ParentID: op.Object.ParentID, Path: op.Path,
		EncName: op.Object.EncName, Folder: op.Folder, Size: result.Size, ModTime: op.Object.ModTime,
		Executable: op.Object.Executable, ContentMAC: result.ContentMAC, BodyID: result.BodyID,
		BodyMD5: result.BodyMD5, DriveID: result.DriveID, PreviousDriveID: op.Object.DriveID, Version: op.Version}
}

func placementOp(op BatchOp) journal.Op {
	kind := journal.OpMove
	if path.Dir(op.FromPath) == path.Dir(op.Path) {
		kind = journal.OpRename
	}
	return journal.Op{Kind: kind, ObjectID: op.ObjectID, ParentID: op.Object.ParentID, From: op.FromPath, To: op.Path,
		EncName: op.Object.EncName, Folder: op.Folder, Version: op.Version}
}

func attrOp(op BatchOp) journal.Op {
	facts := op.Object
	return journal.Op{Kind: journal.OpModify, ObjectID: op.ObjectID, ParentID: facts.ParentID, Path: op.Path,
		EncName: facts.EncName, Folder: op.Folder, Size: facts.Size, ModTime: facts.ModTime, Executable: facts.Executable,
		ContentMAC: facts.ContentMAC, BodyID: facts.BodyID, BodyMD5: facts.BodyMD5, DriveID: facts.DriveID, Version: op.Version}
}

func deleteOp(op BatchOp) journal.Op {
	kind := journal.OpDelete
	switch {
	case op.Superseded:
		kind = journal.OpSuperseded
	case op.Reason == model.ReasonMovedOut:
		kind = journal.OpMovedOut
	}
	trash := &journal.TrashRef{ID: op.TrashID, DriveID: trashDriveID(op), Reason: string(op.Reason), OriginalPath: op.Path,
		Size: op.Object.Size, FileCount: max(op.FileCount, boolCount(!op.Folder)), Folder: op.Folder, ObjectID: op.ObjectID,
		ContentMAC: trashMAC(op)}
	return journal.Op{Kind: kind, ObjectID: op.ObjectID, Path: op.Path, Folder: op.Folder, Reason: string(op.Reason),
		Trash: trash, FileCount: op.FileCount, Version: op.Version}
}

func trashDriveID(op BatchOp) string {
	if op.TrashLatest && op.Result.DriveID != "" {
		return op.Result.DriveID
	}
	return op.Object.DriveID
}

func trashMAC(op BatchOp) []byte {
	if op.TrashLatest && op.Result.ContentMAC != nil {
		return op.Result.ContentMAC
	}
	return op.Object.ContentMAC
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}
