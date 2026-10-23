package syncer

import (
	"context"
	"time"

	"vault/internal/index"
	"vault/internal/model"
)

const (
	maxOpFailures = 3
	deferredDelay = 15 * time.Minute
)

// deferFailingOps takes ops that keep failing out of the batch and back into the queue for a
// later try, so one file Drive refuses cannot hold up every other change. The next try is a
// new batch with fresh IDs and a fresh upload session, which is what Drive asks for when a
// session keeps failing.
func (o *Outgoing) deferFailingOps(ctx context.Context, plan *BatchPlan) error {
	failing := map[model.ObjectID]bool{}
	for _, op := range plan.Ops {
		if !op.Done && op.Failures >= maxOpFailures {
			failing[op.ObjectID] = true
		}
	}
	if len(failing) == 0 {
		return nil
	}
	var kept []BatchOp
	var deferred []int64
	for _, op := range plan.Ops {
		if failing[op.ObjectID] || failing[op.SubsumedBy] {
			deferred = append(deferred, op.Seqs...)
			o.Cache.RemoveSnapshot(plan.ID, op.ObjectID)
			o.Logger.Warn("deferring a change Drive keeps refusing", "oid", op.ObjectID, "kind", op.Kind, "retryIn", deferredDelay)
			continue
		}
		kept = append(kept, op)
	}
	o.batchMu.Lock()
	defer o.batchMu.Unlock()
	plan.Ops = kept
	retryAt := o.Now().Add(deferredDelay)
	return o.persistLocked(ctx, plan, func(tx *index.Tx) error {
		for _, seq := range deferred {
			if err := tx.RescheduleOp(seq, maxOpFailures, retryAt); err != nil {
				return err
			}
		}
		return nil
	})
}
