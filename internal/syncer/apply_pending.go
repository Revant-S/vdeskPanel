package syncer

import (
	"context"
	"errors"

	"github.com/cosmoboy/vault/internal/index"
)

// ApplyPending applies stored remote commits in clock order. A commit waits while any of its
// parents is missing, so a move into a folder created on a third device never arrives
// before that folder does.
func (i *Incoming) ApplyPending(ctx context.Context) error {
	return i.Exclusive(func() error {
		for {
			applied, err := i.applyReadyCommits(ctx)
			if err != nil || applied == 0 {
				return err
			}
		}
	})
}

func (i *Incoming) applyReadyCommits(ctx context.Context) (int, error) {
	var ready []index.CommitRecord
	err := i.Index.View(ctx, func(tx *index.Tx) error {
		unapplied, err := tx.UnappliedCommits()
		if err != nil {
			return err
		}
		for _, record := range unapplied {
			present, err := parentsPresent(tx, record)
			if err != nil {
				return err
			}
			if present {
				ready = append(ready, record)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, record := range ready {
		if err := i.applyCommit(ctx, record); err != nil {
			return 0, err
		}
	}
	return len(ready), nil
}

func parentsPresent(tx *index.Tx, record index.CommitRecord) (bool, error) {
	for _, parentID := range record.Parents {
		parent, err := tx.Commit(parentID)
		if errors.Is(err, index.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !parent.Applied {
			return false, nil
		}
	}
	return true, nil
}

func (i *Incoming) applyCommit(ctx context.Context, record index.CommitRecord) error {
	commit, err := i.Journal.OpenRecord(record)
	if err != nil {
		return err
	}
	if err := i.Applier.ApplyRemoteCommit(ctx, commit); err != nil {
		return err
	}
	return i.Index.Update(ctx, func(tx *index.Tx) error { return tx.MarkCommitApplied(record.ID) })
}
