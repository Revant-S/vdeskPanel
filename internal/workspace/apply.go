package workspace

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/tree"
)

// remoteApply applies one commit from another device. Index changes share one transaction;
// tree changes happen as it goes and are rebuilt from the index if the transaction fails.
// Work that touches the kernel or disk bodies runs only after the transaction commits.
type remoteApply struct {
	ws     *Workspace
	ctx    context.Context
	commit journal.Commit
	tx     *index.Tx
	after  []func()
}

func (w *Workspace) ApplyRemoteCommit(ctx context.Context, commit journal.Commit) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	apply := &remoteApply{ws: w, ctx: ctx, commit: commit}
	err := w.update(ctx, func(tx *index.Tx) error {
		apply.tx = tx
		for _, op := range commit.Ops {
			if err := apply.op(op); err != nil {
				return fmt.Errorf("apply %s %s: %w", op.Kind, op.ObjectID, err)
			}
		}
		return nil
	})
	if err != nil {
		if rebuildErr := w.rebuildTree(ctx); rebuildErr != nil {
			return errors.Join(err, rebuildErr)
		}
		return err
	}
	for _, effect := range apply.after {
		effect()
	}
	return nil
}

func (a *remoteApply) op(op journal.Op) error {
	switch op.Kind {
	case journal.OpCreate:
		return a.create(op)
	case journal.OpModify:
		return a.content(op)
	case journal.OpRename, journal.OpMove:
		return a.placement(op)
	case journal.OpDelete, journal.OpMovedOut:
		if op.Trash == nil {
			return a.removal(op)
		}
		return a.delete(op)
	case journal.OpSuperseded:
		return a.recordTrash(op)
	case journal.OpTrashRestore:
		return a.trashRestore(op)
	case journal.OpTrashPurge:
		return a.trashPurge(op)
	}
	return nil
}

func (a *remoteApply) load(id model.ObjectID) (index.Object, bool, error) {
	object, err := a.tx.Object(id)
	if errors.Is(err, index.ErrNotFound) {
		return index.Object{}, false, nil
	}
	return object, err == nil, err
}

// hasUnsyncedContent reports local content that Drive does not have yet.
func (a *remoteApply) hasUnsyncedContent(object index.Object) (bool, error) {
	if object.State == model.Dirty {
		return true, nil
	}
	ops, err := a.tx.OpsForObject(object.ID)
	for _, op := range ops {
		switch model.LocalOpKind(op.Kind) {
		case model.LocalCreate, model.LocalModify, model.LocalAttr:
			return true, nil
		}
	}
	return false, err
}

func (a *remoteApply) dropOps(id model.ObjectID, kinds ...model.LocalOpKind) error {
	ops, err := a.tx.OpsForObject(id)
	if err != nil {
		return err
	}
	var seqs []int64
	for _, op := range ops {
		if op.BatchID != "" {
			continue
		}
		for _, kind := range kinds {
			if model.LocalOpKind(op.Kind) == kind {
				seqs = append(seqs, op.Seq)
			}
		}
	}
	return a.tx.DeleteOps(seqs)
}

func (a *remoteApply) recordTrash(op journal.Op) error {
	if op.Trash == nil {
		return nil
	}
	trash := op.Trash
	return a.tx.PutTrash(index.TrashRecord{ID: trash.ID, ObjectID: op.ObjectID, OriginalPath: trash.OriginalPath,
		DeletedAt: wallTime(a.commit.Clock), Device: a.commit.Device, Reason: trash.Reason, Size: trash.Size,
		FileCount: trash.FileCount, Folder: trash.Folder, ContentMAC: trash.ContentMAC, DriveID: trash.DriveID})
}

func (a *remoteApply) invalidateEntry(parent model.ObjectID, name string) {
	a.after = append(a.after, func() {
		if ino, ok := a.ws.inodes.existingInode(parent); ok {
			a.ws.queueEntryInvalidation(ino, name)
		}
	})
}

func (a *remoteApply) dropLocalBody(id model.ObjectID) {
	a.after = append(a.after, func() {
		if ino, ok := a.ws.inodes.existingInode(id); ok {
			a.ws.queueContentInvalidation(ino)
		}
		if err := a.ws.Cache.Free(a.ctx, id); err != nil {
			a.ws.Logger.Debug("free replaced body", "oid", id, "err", err)
		}
	})
}

func (w *Workspace) rebuildTree(ctx context.Context) error {
	built, err := tree.Build(ctx, liveRows(ctx, w.Index), w.Names.DecryptName, runtime.GOMAXPROCS(0))
	if err != nil {
		return fmt.Errorf("rebuild tree after failed apply: %w", err)
	}
	w.tree.ReplaceContents(built)
	return nil
}
