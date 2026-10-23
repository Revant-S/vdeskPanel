package index

import (
	"fmt"
	"strings"
	"time"

	"vault/internal/model"
)

type PendingOp struct {
	Seq         int64
	ObjectID    model.ObjectID
	Kind        string
	Payload     []byte
	Stamp       model.Stamp
	NotBefore   time.Time
	Attempts    int
	NextAttempt time.Time
	BatchID     string
}

const opColumns = `seq, object_id, kind, payload, stamp, not_before, attempts, next_attempt, batch_id`

// The payload is bound to the op's stamp, which is unique per device and known before insert.
func (t *Tx) AppendOp(op PendingOp) (int64, error) {
	payload, err := t.seal("pending_ops", "payload", op.Stamp.String(), op.Payload)
	if err != nil {
		return 0, err
	}
	result, err := t.exec(`INSERT INTO pending_ops (object_id, kind, payload, stamp, not_before, attempts, next_attempt, batch_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, op.ObjectID, op.Kind, payload, op.Stamp.String(),
		unixNanos(op.NotBefore), op.Attempts, unixNanos(op.NextAttempt), op.BatchID)
	if err != nil {
		return 0, fmt.Errorf("append op: %w", err)
	}
	return result.LastInsertId()
}

func (t *Tx) UnbatchedOps() ([]PendingOp, error) {
	return t.selectOps(`WHERE batch_id = '' ORDER BY seq`)
}

func (t *Tx) OpsOfBatch(batchID string) ([]PendingOp, error) {
	return t.selectOps(`WHERE batch_id = ? ORDER BY seq`, batchID)
}

func (t *Tx) OpsForObject(id model.ObjectID) ([]PendingOp, error) {
	return t.selectOps(`WHERE object_id = ? ORDER BY seq`, id)
}

func (t *Tx) CountPendingOps() (int, error) {
	var count int
	err := t.queryRow(`SELECT COUNT(*) FROM pending_ops`).Scan(&count)
	return count, err
}

func (t *Tx) AssignBatch(seqs []int64, batchID string) error {
	return t.updateOps(`UPDATE pending_ops SET batch_id = ? WHERE seq IN `, seqs, batchID)
}

func (t *Tx) DeleteOps(seqs []int64) error {
	return t.updateOps(`DELETE FROM pending_ops WHERE seq IN `, seqs)
}

func (t *Tx) DeleteOpsOfBatch(batchID string) error {
	if _, err := t.exec(`DELETE FROM pending_ops WHERE batch_id = ?`, batchID); err != nil {
		return fmt.Errorf("delete batch ops: %w", err)
	}
	return nil
}

func (t *Tx) RescheduleOp(seq int64, attempts int, nextAttempt time.Time) error {
	_, err := t.exec(`UPDATE pending_ops SET attempts = ?, next_attempt = ?, batch_id = '' WHERE seq = ?`,
		attempts, unixNanos(nextAttempt), seq)
	if err != nil {
		return fmt.Errorf("reschedule op: %w", err)
	}
	return nil
}

func (t *Tx) updateOps(statement string, seqs []int64, leading ...any) error {
	if len(seqs) == 0 {
		return nil
	}
	args := leading
	for _, seq := range seqs {
		args = append(args, seq)
	}
	placeholders := "(" + strings.TrimSuffix(strings.Repeat("?,", len(seqs)), ",") + ")"
	if _, err := t.exec(statement+placeholders, args...); err != nil {
		return fmt.Errorf("update ops: %w", err)
	}
	return nil
}

func (t *Tx) selectOps(where string, args ...any) ([]PendingOp, error) {
	rows, err := t.query(`SELECT `+opColumns+` FROM pending_ops `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select ops: %w", err)
	}
	defer rows.Close()
	var ops []PendingOp
	for rows.Next() {
		var op PendingOp
		var stamp string
		var notBefore, nextAttempt int64
		var payload []byte
		if err := rows.Scan(&op.Seq, &op.ObjectID, &op.Kind, &payload, &stamp, &notBefore, &op.Attempts, &nextAttempt, &op.BatchID); err != nil {
			return nil, err
		}
		if op.Stamp, err = model.ParseStamp(stamp); err != nil {
			return nil, err
		}
		op.NotBefore, op.NextAttempt = fromUnixNanos(notBefore), fromUnixNanos(nextAttempt)
		if op.Payload, err = t.open("pending_ops", "payload", stamp, payload); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
