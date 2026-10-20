package tree

import (
	"context"
	"fmt"
	"io/fs"
	"iter"
	"time"

	"github.com/cosmoboy/vault/internal/model"
	"golang.org/x/sync/errgroup"
)

// EncryptedEntry is one index row before its name is decrypted.
type EncryptedEntry struct {
	ID         model.ObjectID
	ParentID   model.ObjectID
	EncName    string
	Kind       model.Kind
	Size       int64
	ModTime    time.Time
	State      model.LocalState
	Executable bool
	LocalOnly  bool
}

// NameDecrypter takes the object ID because it is part of the name's associated data.
type NameDecrypter func(id model.ObjectID, encName string) (string, error)

const decryptBatchSize = 256

// Build decrypts every row's name in parallel and links the rows into a tree. Rows may arrive
// in any order. A row for the root only updates the root's metadata. Duplicate names, unknown
// parents and cycles fail the build, since the index must never hold them.
func Build(ctx context.Context, rows iter.Seq2[EncryptedEntry, error], decryptName NameDecrypter, workers int) (*Tree, error) {
	var encrypted []EncryptedEntry
	for row, err := range rows {
		if err != nil {
			return nil, fmt.Errorf("tree: read rows: %w", err)
		}
		encrypted = append(encrypted, row)
	}
	entries, err := decryptRows(ctx, encrypted, decryptName, workers)
	if err != nil {
		return nil, err
	}
	return linkEntries(entries)
}

func decryptRows(ctx context.Context, rows []EncryptedEntry, decryptName NameDecrypter, workers int) ([]model.Entry, error) {
	entries := make([]model.Entry, len(rows))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(max(workers, 1))
	for start := 0; start < len(rows); start += decryptBatchSize {
		end := min(start+decryptBatchSize, len(rows))
		group.Go(func() error {
			for i := start; i < end; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				entry, err := decryptRow(rows[i], decryptName)
				if err != nil {
					return err
				}
				entries[i] = entry
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return entries, nil
}

func decryptRow(row EncryptedEntry, decryptName NameDecrypter) (model.Entry, error) {
	entry := model.Entry{
		ID: row.ID, ParentID: row.ParentID, Kind: row.Kind, Size: row.Size, ModTime: row.ModTime,
		State: row.State, Executable: row.Executable, LocalOnly: row.LocalOnly,
	}
	if row.ID == model.RootID {
		return entry, nil
	}
	name, err := decryptName(row.ID, row.EncName)
	if err != nil {
		return model.Entry{}, fmt.Errorf("tree: decrypt name of %s: %w", row.ID, err)
	}
	entry.Name = name
	return entry, nil
}

func linkEntries(entries []model.Entry) (*Tree, error) {
	t := New()
	for _, entry := range entries {
		if entry.ID == model.RootID {
			root := t.nodes[model.RootID]
			root.entry.ModTime, root.entry.State = entry.ModTime, entry.State
			continue
		}
		if _, exists := t.nodes[entry.ID]; exists {
			return nil, fmt.Errorf("tree: build: duplicate object %s: %w", entry.ID, fs.ErrExist)
		}
		if err := validateName(entry.Name); err != nil {
			return nil, fmt.Errorf("tree: build: %s: %w", entry.ID, err)
		}
		t.nodes[entry.ID] = newNode(entry)
	}
	for _, entry := range entries {
		if entry.ID == model.RootID {
			continue
		}
		if err := t.attachLocked(entry); err != nil {
			return nil, err
		}
	}
	if reachable := t.countReachableLocked(); reachable != len(t.nodes) {
		return nil, fmt.Errorf("tree: build: %d objects not reachable from root (cycle)", len(t.nodes)-reachable)
	}
	return t, nil
}

func (t *Tree) attachLocked(entry model.Entry) error {
	parent, err := t.folderLocked(entry.ParentID)
	if err != nil {
		return fmt.Errorf("tree: build: %s: %w", entry.ID, err)
	}
	if _, taken := parent.children[entry.Name]; taken {
		return fmt.Errorf("tree: build: %s: name taken in %s: %w", entry.ID, entry.ParentID, fs.ErrExist)
	}
	parent.children[entry.Name] = entry.ID
	return nil
}

func (t *Tree) countReachableLocked() int {
	count := 0
	stack := []model.ObjectID{model.RootID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		count++
		for _, childID := range t.nodes[id].children {
			stack = append(stack, childID)
		}
	}
	return count
}
