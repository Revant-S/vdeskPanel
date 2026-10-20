package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

// formBatch selects ready ops, reduces them and persists the batch with every Drive ID it
// will need, before any Drive call, so replaying it can never create duplicates.
func (o *Outgoing) formBatch(ctx context.Context, ignoreQuiet bool) (*BatchPlan, error) {
	net, err := o.readyNetOps(ctx, ignoreQuiet)
	if err != nil || len(net) == 0 {
		return nil, err
	}
	if err := o.adoptReplacements(ctx, net); err != nil {
		return nil, err
	}
	ops, err := o.batchOps(ctx, net)
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	plan, err := o.newPlan(ctx, ops)
	if err != nil {
		return nil, err
	}
	return plan, o.persistNewBatch(ctx, plan)
}

func (o *Outgoing) readyNetOps(ctx context.Context, ignoreQuiet bool) ([]NetOp, error) {
	var net []NetOp
	err := o.Index.View(ctx, func(tx *index.Tx) error {
		ops, err := tx.UnbatchedOps()
		if err != nil {
			return err
		}
		ready := SelectReady(ops, o.Now(), o.config.Readiness, o.Workspace.HasOpenWriter, ignoreQuiet)
		if len(ready) == 0 {
			return nil
		}
		net, err = ReduceToNetEffect(ready, func(id model.ObjectID) bool { return syncedIn(tx, id) })
		return err
	})
	return net, err
}

func syncedIn(tx *index.Tx, id model.ObjectID) bool {
	object, err := tx.Object(id)
	return err == nil && object.DriveID != ""
}

func (o *Outgoing) adoptReplacements(ctx context.Context, net []NetOp) error {
	for _, op := range net {
		if op.AdoptFrom == "" {
			continue
		}
		if err := o.Workspace.AdoptReplacement(ctx, op.ObjectID, op.AdoptFrom); err != nil {
			return fmt.Errorf("adopt replacement: %w", err)
		}
	}
	return nil
}

// batchOps attaches object facts and cuts the batch to MaxBatch net ops. Ops whose parent is
// neither on Drive nor created in this batch wait for a later batch, as do the subsumed
// children of a folder that did not make the cut.
func (o *Outgoing) batchOps(ctx context.Context, net []NetOp) ([]BatchOp, error) {
	var ops []BatchOp
	err := o.Index.View(ctx, func(tx *index.Tx) error {
		creating := map[model.ObjectID]bool{}
		chosen := map[model.ObjectID]bool{}
		for _, op := range net {
			if op.SubsumedBy != "" || (o.config.Readiness.MaxBatch > 0 && len(chosen) >= o.config.Readiness.MaxBatch) {
				continue
			}
			batchOp, ready, err := o.batchOpFor(tx, op, creating)
			if err != nil {
				return err
			}
			if !ready {
				continue
			}
			if op.Kind == NetCreate {
				creating[op.ObjectID] = true
			}
			chosen[op.ObjectID] = true
			ops = append(ops, batchOp)
		}
		for _, op := range net {
			if op.SubsumedBy != "" && chosen[op.SubsumedBy] {
				batchOp, _, err := o.batchOpFor(tx, op, creating)
				if err != nil {
					return err
				}
				ops = append(ops, batchOp)
			}
		}
		return nil
	})
	return ops, err
}

func (o *Outgoing) batchOpFor(tx *index.Tx, op NetOp, creating map[model.ObjectID]bool) (BatchOp, bool, error) {
	object, err := tx.Object(op.ObjectID)
	if errors.Is(err, index.ErrNotFound) {
		return BatchOp{NetOp: op}, true, nil
	}
	if err != nil {
		return BatchOp{}, false, err
	}
	facts := ObjectFacts{ParentID: object.ParentID, EncName: object.EncName, Size: object.Size, ModTime: object.ModTime,
		Executable: object.Executable, DriveID: object.DriveID, ContentMAC: object.ContentMAC, BodyID: object.BodyID, BodyMD5: object.BodyMD5}
	needsParent := op.Kind == NetCreate || op.Kind == NetMove || op.Kind == NetModify
	if needsParent && object.ParentID != model.RootID && !creating[object.ParentID] && !syncedIn(tx, object.ParentID) {
		return BatchOp{}, false, nil
	}
	return BatchOp{NetOp: op, Object: facts}, true, nil
}

func (o *Outgoing) newPlan(ctx context.Context, ops []BatchOp) (*BatchPlan, error) {
	needed := 1
	for _, op := range ops {
		if needsNewDriveID(op) {
			needed++
		}
	}
	ids, err := o.Remote.GenerateIDs(ctx, needed)
	if err != nil {
		return nil, fmt.Errorf("generate drive ids: %w", err)
	}
	batchID, err := model.NewRandomID(o.Random)
	if err != nil {
		return nil, err
	}
	commitID, err := journal.NewCommitID(o.Random)
	if err != nil {
		return nil, err
	}
	plan := &BatchPlan{ID: batchID, CommitID: commitID, CommitDriveID: ids[0], Ops: ops}
	next := 1
	for i := range plan.Ops {
		if needsNewDriveID(plan.Ops[i]) {
			plan.Ops[i].NewDriveID, next = ids[next], next+1
		}
		if plan.Ops[i].Kind == NetDelete && plan.Ops[i].SubsumedBy == "" {
			if plan.Ops[i].TrashID, err = model.NewRandomID(o.Random); err != nil {
				return nil, err
			}
			plan.Ops[i].TrashID = "t-" + plan.Ops[i].TrashID
		}
	}
	return plan, nil
}

func needsNewDriveID(op BatchOp) bool {
	switch op.Kind {
	case NetCreate, NetModify:
		return true
	case NetDelete:
		return op.TrashLatest && op.SubsumedBy == "" && !op.Folder
	}
	return false
}

func (o *Outgoing) persistNewBatch(ctx context.Context, plan *BatchPlan) error {
	var seqs []int64
	for _, op := range plan.Ops {
		seqs = append(seqs, op.Seqs...)
	}
	return o.saveBatchWith(ctx, plan, func(tx *index.Tx) error { return tx.AssignBatch(seqs, plan.ID) })
}
