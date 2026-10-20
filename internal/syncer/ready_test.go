package syncer

import (
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

var readiness = ReadinessConfig{QuietPeriod: 2 * time.Second, DeleteHold: 3 * time.Second, MaxDeleteHold: 30 * time.Second, MaxBatch: 200}

func pending(seq int64, id model.ObjectID, kind model.LocalOpKind, notBefore time.Time) index.PendingOp {
	return index.PendingOp{Seq: seq, ObjectID: id, Kind: string(kind), NotBefore: notBefore}
}

func noWriters(model.ObjectID) bool { return false }

func TestQuietPeriodHoldsTheWholeBatch(t *testing.T) {
	start := time.Unix(1000, 0)
	ops := []index.PendingOp{pending(1, "a", model.LocalModify, start), pending(2, "b", model.LocalModify, start.Add(time.Second))}
	if ready := SelectReady(ops, start.Add(500*time.Millisecond), readiness, noWriters, false); len(ready) != 0 {
		t.Fatalf("batch formed while ops still arriving: %v", ready)
	}
	if ready := SelectReady(ops, start.Add(time.Second), readiness, noWriters, false); len(ready) != 2 {
		t.Fatalf("ready = %d, want 2", len(ready))
	}
}

func TestOpenWriterHoldsOnlyItsObject(t *testing.T) {
	start := time.Unix(1000, 0)
	ops := []index.PendingOp{pending(1, "a", model.LocalCreate, start), pending(2, "b", model.LocalModify, start), pending(3, "a", model.LocalModify, start)}
	ready := SelectReady(ops, start, readiness, func(id model.ObjectID) bool { return id == "a" }, false)
	if len(ready) != 1 || ready[0].ObjectID != "b" {
		t.Fatalf("ready = %v", ready)
	}
}

func TestDeletesWaitUntilNoneArriveForTheHold(t *testing.T) {
	start := time.Unix(1000, 0)
	var ops []index.PendingOp
	for i := int64(0); i < 25; i++ {
		ops = append(ops, pending(i+1, model.ObjectID(rune('a'+i)), model.LocalDelete, start.Add(time.Duration(i)*100*time.Millisecond+readiness.DeleteHold)))
	}
	ops = append(ops, pending(26, "folder", model.LocalDelete, start.Add(2600*time.Millisecond+readiness.DeleteHold)))
	if ready := SelectReady(ops, start.Add(4*time.Second), readiness, noWriters, false); len(ready) != 0 {
		t.Fatalf("some deletes released early: %d", len(ready))
	}
	if ready := SelectReady(ops, start.Add(5700*time.Millisecond), readiness, noWriters, false); len(ready) != 26 {
		t.Fatalf("deletes released = %d, want all 26 together", len(ready))
	}
}

func TestEndlessDeletesAreCappedByMaxHold(t *testing.T) {
	start := time.Unix(1000, 0)
	var ops []index.PendingOp
	for i := int64(0); i < 40; i++ {
		ops = append(ops, pending(i+1, model.ObjectID(rune('A'+i)), model.LocalDelete, start.Add(time.Duration(i)*time.Second+readiness.DeleteHold)))
	}
	if ready := SelectReady(ops, start.Add(31*time.Second), readiness, noWriters, false); len(ready) != 40 {
		t.Fatalf("max hold did not release deletes: %d", len(ready))
	}
}

func TestDrainIgnoresQuietAndHolds(t *testing.T) {
	start := time.Unix(1000, 0)
	ops := []index.PendingOp{pending(1, "a", model.LocalModify, start.Add(time.Hour)), pending(2, "b", model.LocalDelete, start.Add(time.Hour))}
	if ready := SelectReady(ops, start, readiness, noWriters, true); len(ready) != 2 {
		t.Fatalf("drain ready = %d", len(ready))
	}
}

func TestRetryBackoffHoldsObject(t *testing.T) {
	start := time.Unix(1000, 0)
	op := pending(1, "a", model.LocalModify, start)
	op.NextAttempt = start.Add(time.Minute)
	if ready := SelectReady([]index.PendingOp{op}, start, readiness, noWriters, true); len(ready) != 0 {
		t.Fatal("op in backoff was selected")
	}
}
