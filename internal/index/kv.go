package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bits-and-blooms/bitset"
	"github.com/cosmoboy/vault/internal/model"
)

func (t *Tx) KV(key string) ([]byte, bool, error) {
	var value []byte
	err := t.queryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read kv %s: %w", key, err)
	}
	return value, true, nil
}

func (t *Tx) SetKV(key string, value []byte) error {
	if _, err := t.exec(`INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)`, key, value); err != nil {
		return fmt.Errorf("write kv %s: %w", key, err)
	}
	return nil
}

func (t *Tx) DeleteKV(key string) error {
	_, err := t.exec(`DELETE FROM kv WHERE key = ?`, key)
	return err
}

// SegmentBitmap returns nil when no bitmap is stored, which the cache reads as a complete,
// locally authored body rather than a sparse mirror.
func (s *Store) SegmentBitmap(ctx context.Context, id model.ObjectID) (*bitset.BitSet, error) {
	var present *bitset.BitSet
	err := s.View(ctx, func(tx *Tx) error {
		var data []byte
		err := tx.queryRow(`SELECT bitmap FROM segments WHERE object_id = ?`, id).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		present = bitset.New(0)
		return present.UnmarshalBinary(data)
	})
	if err != nil {
		return nil, fmt.Errorf("read segment bitmap: %w", err)
	}
	return present, nil
}

// PutSegmentBitmap with nil deletes the stored bitmap.
func (s *Store) PutSegmentBitmap(ctx context.Context, id model.ObjectID, present *bitset.BitSet) error {
	if present == nil {
		return s.Update(ctx, func(tx *Tx) error { return tx.DeleteSegmentBitmap(id) })
	}
	data, err := present.MarshalBinary()
	if err != nil {
		return err
	}
	return s.Update(ctx, func(tx *Tx) error {
		_, err := tx.exec(`INSERT OR REPLACE INTO segments (object_id, bitmap) VALUES (?, ?)`, id, data)
		return err
	})
}

func (t *Tx) DeleteSegmentBitmap(id model.ObjectID) error {
	_, err := t.exec(`DELETE FROM segments WHERE object_id = ?`, id)
	return err
}
