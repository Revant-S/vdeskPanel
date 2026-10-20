// Package restore downloads full bodies in the background until the device holds a complete
// copy of the vault, or only the bodies someone asked for.
package restore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

// BodyCache opens cached bodies; a CloudOnly or Partial entry fetches what it is missing.
type BodyCache interface {
	Open(ctx context.Context, id model.ObjectID, plaintextSize int64, state model.LocalState, expectedBodyID vaultfile.BodyID) (*cache.Entry, error)
}

type Mode int

const (
	// RestoreEverything downloads every live body, wanted ones first.
	RestoreEverything Mode = iota
	// OnDemand downloads only bodies passed to Want or RequestLocal.
	OnDemand
)

type Deps struct {
	Index *index.Store
	Cache BodyCache
	// UploadsPending reports local changes not yet on Drive; restore yields to them (D38).
	UploadsPending func() bool
	// OnLocal, if set, learns of every object this restorer made Local.
	OnLocal func(id model.ObjectID)
	Logger  *slog.Logger
}

type Config struct {
	Mode          Mode
	Files         int
	RangesPerFile int
	// YieldInterval is how often a yielding restore checks whether uploads are done.
	YieldInterval time.Duration
	// RetryFailedAfter is how long a failed file waits before Run tries it again.
	RetryFailedAfter time.Duration
}

type Restorer struct {
	deps      Deps
	config    Config
	kick      chan struct{}
	runMu     sync.Mutex
	persistMu sync.Mutex

	mu          sync.Mutex
	paused      bool
	wanted      map[model.ObjectID]bool
	failures    map[model.ObjectID]failure
	progress    Progress
	cancelRound context.CancelFunc
}

type failure struct {
	err error
	at  time.Time
}

// New loads the persisted pause flag and wanted set, so a restart continues where it stopped.
func New(ctx context.Context, deps Deps, config Config) (*Restorer, error) {
	if config.Files < 1 {
		config.Files = 4
	}
	if config.RangesPerFile < 1 {
		config.RangesPerFile = 4
	}
	if config.YieldInterval <= 0 {
		config.YieldInterval = 250 * time.Millisecond
	}
	if config.RetryFailedAfter <= 0 {
		config.RetryFailedAfter = time.Minute
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	r := &Restorer{deps: deps, config: config, kick: make(chan struct{}, 1), failures: map[model.ObjectID]failure{}}
	if err := r.loadControl(ctx); err != nil {
		return nil, err
	}
	r.progress.Paused = r.paused
	return r, nil
}

// Kick wakes Run to look for work at once.
func (r *Restorer) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run keeps restoring until ctx ends, waking on Kick and to retry failed files.
func (r *Restorer) Run(ctx context.Context) error {
	for {
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.deps.Logger.Warn("restore round incomplete", "err", err)
		}
		retry := time.NewTimer(r.config.RetryFailedAfter)
		select {
		case <-ctx.Done():
			retry.Stop()
			return nil
		case <-r.kick:
		case <-retry.C:
		}
		retry.Stop()
	}
}

// RunOnce downloads everything currently due and returns the failures of individual files
// joined together. A pause ends the round early without an error.
func (r *Restorer) RunOnce(ctx context.Context) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	roundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !r.beginRound(cancel) {
		return nil
	}
	defer r.endRound()
	plan, err := r.planRound(roundCtx)
	if err != nil {
		return err
	}
	r.startProgress(plan)
	var group errgroup.Group
	group.SetLimit(r.config.Files)
	var mu sync.Mutex
	var failed []error
	for _, object := range plan.pending {
		if !r.waitTurn(roundCtx) {
			break
		}
		group.Go(func() error {
			if err := r.restoreOne(roundCtx, object); err != nil {
				mu.Lock()
				failed = append(failed, err)
				mu.Unlock()
			}
			return nil
		})
	}
	group.Wait()
	r.finishProgress()
	return errors.Join(append(failed, ctx.Err())...)
}

// beginRound registers the round so Pause can cancel it; a paused restorer starts none.
func (r *Restorer) beginRound(cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return false
	}
	r.cancelRound = cancel
	return true
}

func (r *Restorer) endRound() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelRound = nil
}

// waitTurn holds the next file back while uploads are pending, and refuses once paused.
func (r *Restorer) waitTurn(ctx context.Context) bool {
	for {
		if ctx.Err() != nil || r.isPaused() {
			return false
		}
		if r.deps.UploadsPending == nil || !r.deps.UploadsPending() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(r.config.YieldInterval):
		}
	}
}

func (r *Restorer) restoreOne(ctx context.Context, object index.Object) error {
	restored, err := r.download(ctx, object)
	switch {
	case err == nil && restored:
		r.fileDone(object)
		if r.deps.OnLocal != nil {
			r.deps.OnLocal(object.ID)
		}
		return nil
	case err == nil, ctx.Err() != nil:
		return nil
	}
	r.recordFailure(object.ID, err)
	r.deps.Logger.Warn("restore file failed", "oid", object.ID, "err", err)
	return fmt.Errorf("restore %s: %w", object.ID, err)
}
