package index

import (
	"fmt"
	"time"
)

type Batch struct {
	ID        string
	CommitID  string
	Payload   []byte
	State     string
	CreatedAt time.Time
}

func (t *Tx) PutBatch(b Batch) error {
	payload, err := t.seal("outgoing_batches", "payload", b.ID, b.Payload)
	if err != nil {
		return err
	}
	_, err = t.exec(`INSERT OR REPLACE INTO outgoing_batches (id, commit_id, payload, state, created_at) VALUES (?, ?, ?, ?, ?)`,
		b.ID, b.CommitID, payload, b.State, unixNanos(b.CreatedAt))
	if err != nil {
		return fmt.Errorf("put batch: %w", err)
	}
	return nil
}

func (t *Tx) Batch(id string) (Batch, error) {
	batches, err := t.selectBatches(`WHERE id = ?`, id)
	if err != nil {
		return Batch{}, err
	}
	if len(batches) == 0 {
		return Batch{}, fmt.Errorf("batch %s: %w", id, ErrNotFound)
	}
	return batches[0], nil
}

func (t *Tx) Batches() ([]Batch, error) {
	return t.selectBatches(`ORDER BY created_at, id`)
}

func (t *Tx) DeleteBatch(id string) error {
	if _, err := t.exec(`DELETE FROM outgoing_batches WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}
	return nil
}

func (t *Tx) selectBatches(where string, args ...any) ([]Batch, error) {
	rows, err := t.query(`SELECT id, commit_id, payload, state, created_at FROM outgoing_batches `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select batches: %w", err)
	}
	defer rows.Close()
	var batches []Batch
	for rows.Next() {
		var b Batch
		var payload []byte
		var created int64
		if err := rows.Scan(&b.ID, &b.CommitID, &payload, &b.State, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = fromUnixNanos(created)
		if b.Payload, err = t.open("outgoing_batches", "payload", b.ID, payload); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}
