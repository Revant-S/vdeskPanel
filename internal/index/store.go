// Package index is the durable SQLite copy of the vault metadata, the outgoing queue and the log.
package index

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("index: not found")

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Sealer encrypts columns that could otherwise hold plaintext names or paths.
type Sealer interface {
	Seal(additionalData, plaintext []byte) ([]byte, error)
	Open(additionalData, sealed []byte) ([]byte, error)
}

// Store keeps one writer connection, so writes never contend, and a small reader pool,
// which WAL lets run alongside the writer.
type Store struct {
	writer *sql.DB
	reader *sql.DB
	sealer Sealer
}

func Open(ctx context.Context, path string, sealer Sealer) (*Store, error) {
	writer, err := openDatabase(path, "immediate", 1)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}
	reader, err := openDatabase(path, "deferred", 4)
	if err != nil {
		writer.Close()
		return nil, err
	}
	return &Store{writer: writer, reader: reader, sealer: sealer}, nil
}

func openDatabase(path, txLock string, connections int) (*sql.DB, error) {
	params := url.Values{}
	for _, pragma := range []string{"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(10000)", "foreign_keys(ON)"} {
		params.Add("_pragma", pragma)
	}
	params.Set("_txlock", txLock)
	db, err := sql.Open("sqlite", "file:"+path+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	db.SetMaxOpenConns(connections)
	return db, nil
}

func (s *Store) Close() error {
	return errors.Join(s.reader.Close(), s.writer.Close())
}

// Update runs fn in one write transaction. Calls must not nest.
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	return runTransaction(ctx, s.writer, s.sealer, fn)
}

func (s *Store) View(ctx context.Context, fn func(*Tx) error) error {
	return runTransaction(ctx, s.reader, s.sealer, fn)
}

func runTransaction(ctx context.Context, db *sql.DB, sealer Sealer, fn func(*Tx) error) error {
	sqlTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin index transaction: %w", err)
	}
	if err := fn(&Tx{ctx: ctx, tx: sqlTx, sealer: sealer}); err != nil {
		sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit index transaction: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for number := version; number < len(names); number++ {
		script, err := migrationFiles.ReadFile(names[number])
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, db, string(script), number+1); err != nil {
			return fmt.Errorf("migration %s: %w", names[number], err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, script string, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, script); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
