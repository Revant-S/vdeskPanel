package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"vault/internal/crypto"
)

type Tx struct {
	ctx    context.Context
	tx     *sql.Tx
	sealer Sealer
}

func (t *Tx) exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

func (t *Tx) query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.ctx, query, args...)
}

func (t *Tx) queryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

func (t *Tx) seal(table, column, primaryKey string, plaintext []byte) ([]byte, error) {
	if plaintext == nil {
		return nil, nil
	}
	sealed, err := t.sealer.Seal(crypto.IndexAD(table, column, primaryKey), plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal %s.%s: %w", table, column, err)
	}
	return sealed, nil
}

func (t *Tx) open(table, column, primaryKey string, sealed []byte) ([]byte, error) {
	if sealed == nil {
		return nil, nil
	}
	plaintext, err := t.sealer.Open(crypto.IndexAD(table, column, primaryKey), sealed)
	if err != nil {
		return nil, fmt.Errorf("open %s.%s: %w", table, column, err)
	}
	return plaintext, nil
}

func notFoundOr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func unixNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func fromUnixNanos(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
