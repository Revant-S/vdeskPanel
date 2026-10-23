package restore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"vault/internal/index"
	"vault/internal/model"
)

const (
	pausedKey = "restore.paused"
	wantedKey = "restore.wanted"
)

func (r *Restorer) loadControl(ctx context.Context) error {
	var wanted []model.ObjectID
	err := r.deps.Index.View(ctx, func(tx *index.Tx) error {
		_, paused, err := tx.KV(pausedKey)
		if err != nil {
			return err
		}
		r.paused = paused
		data, found, err := tx.KV(wantedKey)
		if err != nil || !found {
			return err
		}
		return json.Unmarshal(data, &wanted)
	})
	if err != nil {
		return fmt.Errorf("load restore state: %w", err)
	}
	r.wanted = make(map[model.ObjectID]bool, len(wanted))
	for _, id := range wanted {
		r.wanted[id] = true
	}
	return nil
}

// Pause stops the restore, cancelling downloads in flight, and stays paused across restarts.
// Finished segments are kept, so a resumed restore continues where it stopped.
func (r *Restorer) Pause(ctx context.Context) error {
	err := r.deps.Index.Update(ctx, func(tx *index.Tx) error { return tx.SetKV(pausedKey, []byte{1}) })
	if err != nil {
		return fmt.Errorf("pause restore: %w", err)
	}
	r.mu.Lock()
	r.paused, r.progress.Paused, r.progress.Complete = true, true, false
	if r.cancelRound != nil {
		r.cancelRound()
	}
	r.mu.Unlock()
	return nil
}

func (r *Restorer) Resume(ctx context.Context) error {
	if err := r.deps.Index.Update(ctx, func(tx *index.Tx) error { return tx.DeleteKV(pausedKey) }); err != nil {
		return fmt.Errorf("resume restore: %w", err)
	}
	r.mu.Lock()
	r.paused, r.progress.Paused = false, false
	r.mu.Unlock()
	r.Kick()
	return nil
}

func (r *Restorer) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// Want asks for the full bodies of the given files, ahead of the rest of a restore. Earlier
// failures of these files are forgotten, so they are tried again at once.
func (r *Restorer) Want(ctx context.Context, ids ...model.ObjectID) error {
	r.mu.Lock()
	for _, id := range ids {
		r.wanted[id] = true
		delete(r.failures, id)
	}
	r.mu.Unlock()
	if err := r.persistWanted(ctx); err != nil {
		return err
	}
	r.Kick()
	return nil
}

// RequestLocal is the Download action: files are wanted as given, folders for every live
// file below them.
func (r *Restorer) RequestLocal(ctx context.Context, ids []model.ObjectID) error {
	var files []model.ObjectID
	err := r.deps.Index.View(ctx, func(tx *index.Tx) error {
		for _, id := range ids {
			found, err := liveFilesUnder(tx, id)
			if err != nil {
				return err
			}
			files = append(files, found...)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("request local: %w", err)
	}
	return r.Want(ctx, files...)
}

func liveFilesUnder(tx *index.Tx, id model.ObjectID) ([]model.ObjectID, error) {
	object, err := tx.Object(id)
	if errors.Is(err, index.ErrNotFound) && id == model.RootID {
		object = index.Object{ID: model.RootID, Kind: model.KindFolder}
	} else if err != nil {
		return nil, err
	}
	if object.Kind == model.KindFile {
		return []model.ObjectID{id}, nil
	}
	children, err := tx.ChildrenOf(id)
	if err != nil {
		return nil, err
	}
	var files []model.ObjectID
	for _, child := range children {
		if child.TrashID != "" || (child.Kind == model.KindFile && !isLiveFile(child)) {
			continue
		}
		below, err := liveFilesUnder(tx, child.ID)
		if err != nil {
			return nil, err
		}
		files = append(files, below...)
	}
	return files, nil
}

// settleWanted forgets wanted files that no longer need a body.
func (r *Restorer) settleWanted(ctx context.Context, wanted, outstanding map[model.ObjectID]bool) error {
	changed := false
	r.mu.Lock()
	for id := range wanted {
		if !outstanding[id] && r.wanted[id] {
			delete(r.wanted, id)
			changed = true
		}
	}
	r.mu.Unlock()
	if !changed {
		return nil
	}
	return r.persistWanted(ctx)
}

func (r *Restorer) persistWanted(ctx context.Context) error {
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	ids := make([]model.ObjectID, 0, len(r.wanted))
	for id := range r.wanted {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	slices.Sort(ids)
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	err = r.deps.Index.Update(ctx, func(tx *index.Tx) error {
		if len(ids) == 0 {
			return tx.DeleteKV(wantedKey)
		}
		return tx.SetKV(wantedKey, data)
	})
	if err != nil {
		return fmt.Errorf("store wanted files: %w", err)
	}
	return nil
}
