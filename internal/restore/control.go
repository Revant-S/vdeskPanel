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
	pausedKey      = "restore.paused"
	wantedKey      = "restore.wanted"
	keptInCloudKey = "restore.kept_in_cloud"
)

func (r *Restorer) loadControl(ctx context.Context) error {
	err := r.deps.Index.View(ctx, func(tx *index.Tx) error {
		_, paused, err := tx.KV(pausedKey)
		if err != nil {
			return err
		}
		r.paused = paused
		if r.wanted, err = loadSet(tx, wantedKey); err != nil {
			return err
		}
		r.keptInCloud, err = loadSet(tx, keptInCloudKey)
		return err
	})
	if err != nil {
		return fmt.Errorf("load restore state: %w", err)
	}
	return nil
}

func loadSet(tx *index.Tx, key string) (map[model.ObjectID]bool, error) {
	set := map[model.ObjectID]bool{}
	data, found, err := tx.KV(key)
	if err != nil || !found {
		return set, err
	}
	var ids []model.ObjectID
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
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
		delete(r.keptInCloud, id)
		delete(r.failures, id)
	}
	r.mu.Unlock()
	if err := r.persistSets(ctx); err != nil {
		return err
	}
	r.Kick()
	return nil
}

// RequestLocal is the Download action: files are wanted as given, folders for every live
// file below them.
func (r *Restorer) RequestLocal(ctx context.Context, ids []model.ObjectID) error {
	files, err := r.filesUnder(ctx, ids)
	if err != nil {
		return fmt.Errorf("request local: %w", err)
	}
	return r.Want(ctx, files...)
}

// KeepInCloud is the Free up space action: the files stop being wanted, a restore of
// everything leaves them alone, and downloads in flight stop, so a freed folder stays freed
// until it is opened or downloaded again.
func (r *Restorer) KeepInCloud(ctx context.Context, ids []model.ObjectID) error {
	files, err := r.filesUnder(ctx, ids)
	if err != nil {
		return fmt.Errorf("keep in cloud: %w", err)
	}
	r.mu.Lock()
	for _, id := range files {
		delete(r.wanted, id)
		r.keptInCloud[id] = true
	}
	if r.cancelRound != nil {
		r.cancelRound()
	}
	r.mu.Unlock()
	if err := r.persistSets(ctx); err != nil {
		return err
	}
	r.Kick()
	return nil
}

func (r *Restorer) filesUnder(ctx context.Context, ids []model.ObjectID) ([]model.ObjectID, error) {
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
	return files, err
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
	return r.persistSets(ctx)
}

func (r *Restorer) persistSets(ctx context.Context) error {
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.mu.Lock()
	wanted, kept := sortedIDs(r.wanted), sortedIDs(r.keptInCloud)
	r.mu.Unlock()
	err := r.deps.Index.Update(ctx, func(tx *index.Tx) error {
		if err := storeSet(tx, wantedKey, wanted); err != nil {
			return err
		}
		return storeSet(tx, keptInCloudKey, kept)
	})
	if err != nil {
		return fmt.Errorf("store restore choices: %w", err)
	}
	return nil
}

func sortedIDs(set map[model.ObjectID]bool) []model.ObjectID {
	ids := make([]model.ObjectID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func storeSet(tx *index.Tx, key string, ids []model.ObjectID) error {
	if len(ids) == 0 {
		return tx.DeleteKV(key)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return tx.SetKV(key, data)
}
