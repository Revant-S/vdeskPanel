package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"vault/internal/index"
	"vault/internal/model"
)

// BatchOp is one net op with everything its execution decided so far. It is persisted after
// each step, so a restart resumes exactly where the crash happened.
type BatchOp struct {
	NetOp
	Object          ObjectFacts   `json:"object"`
	NewDriveID      string        `json:"newDriveId,omitempty"`
	TrashID         string        `json:"trashId,omitempty"`
	Snapshot        *SnapshotInfo `json:"snapshot,omitempty"`
	Session         UploadSession `json:"session,omitzero"`
	Result          ContentResult `json:"result,omitzero"`
	Done            bool          `json:"done,omitempty"`
	Failures        int           `json:"failures,omitempty"`
	SessionFailures int           `json:"sessionFailures,omitempty"`
	Skipped         bool          `json:"skipped,omitempty"`
}

// ObjectFacts is the object as it was when the batch formed.
type ObjectFacts struct {
	ParentID   model.ObjectID `json:"parent"`
	EncName    string         `json:"encName"`
	Size       int64          `json:"size"`
	ModTime    time.Time      `json:"mtime"`
	Executable bool           `json:"exec,omitempty"`
	DriveID    string         `json:"driveId,omitempty"`
	ContentMAC []byte         `json:"mac,omitempty"`
	BodyID     []byte         `json:"bodyId,omitempty"`
	BodyMD5    string         `json:"md5,omitempty"`
}

type SnapshotInfo struct {
	Path       string `json:"path"`
	MD5        string `json:"md5"`
	Size       int64  `json:"size"`
	PlainSize  int64  `json:"plainSize"`
	BodyID     []byte `json:"bodyId"`
	ContentMAC []byte `json:"mac"`
}

// ContentResult is what Drive holds for the object once the op is done.
type ContentResult struct {
	DriveID    string `json:"driveId,omitempty"`
	BodyID     []byte `json:"bodyId,omitempty"`
	BodyMD5    string `json:"md5,omitempty"`
	ContentMAC []byte `json:"mac,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Copied     bool   `json:"copied,omitempty"`
}

type BatchPlan struct {
	ID            string    `json:"id"`
	CommitID      string    `json:"commitId"`
	CommitDriveID string    `json:"commitDriveId"`
	Ops           []BatchOp `json:"ops"`
	Committed     bool      `json:"committed,omitempty"`
}

func (b *BatchPlan) find(id model.ObjectID) *BatchOp {
	for i := range b.Ops {
		if b.Ops[i].ObjectID == id && (b.Ops[i].Kind == NetCreate || b.Ops[i].Kind == NetModify) {
			return &b.Ops[i]
		}
	}
	return nil
}

func (o *Outgoing) saveBatch(ctx context.Context, plan *BatchPlan) error {
	return o.saveBatchWith(ctx, plan, nil)
}

// saveBatchWith persists plan and runs also in the same transaction.
func (o *Outgoing) saveBatchWith(ctx context.Context, plan *BatchPlan, also func(*index.Tx) error) error {
	o.batchMu.Lock()
	defer o.batchMu.Unlock()
	return o.persistLocked(ctx, plan, also)
}

// checkpoint records progress of one op. Ops run in parallel on their own copies and only
// touch the shared plan here, under the lock.
func (o *Outgoing) checkpoint(ctx context.Context, plan *BatchPlan, i int, op BatchOp) error {
	o.batchMu.Lock()
	defer o.batchMu.Unlock()
	plan.Ops[i] = op
	return o.persistLocked(ctx, plan, nil)
}

func (o *Outgoing) createdDriveID(plan *BatchPlan, id model.ObjectID) (string, bool) {
	o.batchMu.Lock()
	defer o.batchMu.Unlock()
	if created := plan.find(id); created != nil && created.Kind == NetCreate {
		return created.NewDriveID, true
	}
	return "", false
}

func (o *Outgoing) persistLocked(ctx context.Context, plan *BatchPlan, also func(*index.Tx) error) error {
	payload, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return o.Index.Update(ctx, func(tx *index.Tx) error {
		if err := tx.PutBatch(index.Batch{ID: plan.ID, CommitID: plan.CommitID, Payload: payload, State: "open", CreatedAt: o.Now()}); err != nil {
			return err
		}
		if also != nil {
			return also(tx)
		}
		return nil
	})
}

func (o *Outgoing) loadOpenBatches(ctx context.Context) ([]*BatchPlan, error) {
	var plans []*BatchPlan
	err := o.Index.View(ctx, func(tx *index.Tx) error {
		batches, err := tx.Batches()
		for _, batch := range batches {
			var plan BatchPlan
			if err := json.Unmarshal(batch.Payload, &plan); err != nil {
				return fmt.Errorf("decode batch %s: %w", batch.ID, err)
			}
			plans = append(plans, &plan)
		}
		return err
	})
	return plans, err
}

// BatchDriveIDs returns every Drive ID that open batches have reserved, created or are about
// to replace, so a sweep of Drive never mistakes them for orphans.
func (o *Outgoing) BatchDriveIDs(ctx context.Context) (map[string]bool, error) {
	plans, err := o.loadOpenBatches(ctx)
	ids := map[string]bool{}
	for _, plan := range plans {
		for _, op := range plan.Ops {
			for _, id := range []string{op.NewDriveID, op.Object.DriveID, op.Result.DriveID} {
				if id != "" {
					ids[id] = true
				}
			}
		}
	}
	return ids, err
}
