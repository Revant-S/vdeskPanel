package restore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

type roundPlan struct {
	pending    []index.Object
	doneFiles  int
	doneBytes  int64
	totalFiles int
	totalBytes int64
}

type selection struct {
	wanted      map[model.ObjectID]bool
	failedSince map[model.ObjectID]time.Time
}

// planRound scans the index for the files this round should download. Wanted files come
// first, then smaller before larger, so the most files become usable soonest.
func (r *Restorer) planRound(ctx context.Context) (roundPlan, error) {
	chosen := r.currentSelection()
	var plan roundPlan
	outstanding := map[model.ObjectID]bool{}
	err := r.deps.Index.View(ctx, func(tx *index.Tx) error {
		for object, err := range tx.AllObjects() {
			if err != nil {
				return err
			}
			if r.addToPlan(&plan, chosen, object) {
				outstanding[object.ID] = true
			}
		}
		return nil
	})
	if err != nil {
		return roundPlan{}, fmt.Errorf("plan restore: %w", err)
	}
	slices.SortFunc(plan.pending, func(a, b index.Object) int {
		if aWanted, bWanted := chosen.wanted[a.ID], chosen.wanted[b.ID]; aWanted != bWanted {
			if aWanted {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.Size, b.Size), cmp.Compare(a.ID, b.ID))
	})
	if err := r.settleWanted(ctx, chosen.wanted, outstanding); err != nil {
		return roundPlan{}, err
	}
	return plan, nil
}

// addToPlan counts object toward the round and reports whether it still needs a body.
func (r *Restorer) addToPlan(plan *roundPlan, chosen selection, object index.Object) bool {
	if !isLiveFile(object) || !(r.config.Mode == RestoreEverything || chosen.wanted[object.ID]) {
		return false
	}
	if needsBody(object) && !hasRemoteBody(object) {
		return false
	}
	plan.totalFiles, plan.totalBytes = plan.totalFiles+1, plan.totalBytes+object.Size
	if !needsBody(object) {
		plan.doneFiles, plan.doneBytes = plan.doneFiles+1, plan.doneBytes+object.Size
		return false
	}
	if failedAt, failed := chosen.failedSince[object.ID]; !failed || time.Since(failedAt) >= r.config.RetryFailedAfter {
		plan.pending = append(plan.pending, object)
	}
	return true
}

func (r *Restorer) currentSelection() selection {
	r.mu.Lock()
	defer r.mu.Unlock()
	chosen := selection{wanted: make(map[model.ObjectID]bool, len(r.wanted)), failedSince: map[model.ObjectID]time.Time{}}
	for id := range r.wanted {
		chosen.wanted[id] = true
	}
	for id, failed := range r.failures {
		chosen.failedSince[id] = failed.at
	}
	return chosen
}

// isLiveFile applies the delete tombstone rule: content newer than the delete keeps a file.
func isLiveFile(object index.Object) bool {
	return object.Kind == model.KindFile && !object.LocalOnly && object.TrashID == "" &&
		(object.DeleteVer.IsZero() || object.ContentVer.After(object.DeleteVer))
}

func needsBody(object index.Object) bool {
	return object.State == model.CloudOnly || object.State == model.Partial
}

func hasRemoteBody(object index.Object) bool {
	return object.DriveID != "" && len(object.BodyID) == len(vaultfile.BodyID{})
}
