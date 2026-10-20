package syncer

import (
	"time"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

type ReadinessConfig struct {
	QuietPeriod   time.Duration
	DeleteHold    time.Duration
	MaxDeleteHold time.Duration
	MaxBatch      int
}

// SelectReady picks the ops a batch may take now. An object is taken only with all of its
// ops. Deletes are held together until no delete has arrived for DeleteHold, so a folder's
// own delete is never split from its children's.
func SelectReady(ops []index.PendingOp, now time.Time, config ReadinessConfig, hasWriter func(model.ObjectID) bool, ignoreQuiet bool) []index.PendingOp {
	if len(ops) == 0 {
		return nil
	}
	deletesReady := ignoreQuiet || deletesReleased(ops, now, config)
	if !ignoreQuiet && !batchQuiet(ops, now, config) {
		return nil
	}
	blocked := map[model.ObjectID]bool{}
	for _, op := range ops {
		switch {
		case hasWriter(op.ObjectID):
			blocked[op.ObjectID] = true
		case op.NextAttempt.After(now):
			blocked[op.ObjectID] = true
		case isDelete(op) && !deletesReady:
			blocked[op.ObjectID] = true
		case !isDelete(op) && !ignoreQuiet && op.NotBefore.After(now):
			blocked[op.ObjectID] = true
		}
	}
	var ready []index.PendingOp
	for _, op := range ops {
		if !blocked[op.ObjectID] {
			ready = append(ready, op)
		}
	}
	return ready
}

// batchQuiet holds a batch until nothing new has arrived for the quiet period, unless enough
// work is already waiting to fill one.
func batchQuiet(ops []index.PendingOp, now time.Time, config ReadinessConfig) bool {
	if config.MaxBatch > 0 && len(ops) >= config.MaxBatch {
		return true
	}
	newest := ops[0].NotBefore
	for _, op := range ops {
		if !isDelete(op) && op.NotBefore.After(newest) {
			newest = op.NotBefore
		}
	}
	return !newest.After(now)
}

func deletesReleased(ops []index.PendingOp, now time.Time, config ReadinessConfig) bool {
	var newest, oldest time.Time
	for _, op := range ops {
		if !isDelete(op) {
			continue
		}
		if newest.IsZero() || op.NotBefore.After(newest) {
			newest = op.NotBefore
		}
		if oldest.IsZero() || op.NotBefore.Before(oldest) {
			oldest = op.NotBefore
		}
	}
	if newest.IsZero() {
		return true
	}
	capped := oldest.Add(config.MaxDeleteHold - config.DeleteHold)
	return !newest.After(now) || !capped.After(now)
}

func isDelete(op index.PendingOp) bool {
	return op.Kind == string(model.LocalDelete)
}
