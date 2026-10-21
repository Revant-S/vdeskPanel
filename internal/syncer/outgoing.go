package syncer

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

// LocalWorkspace is what the pipeline needs from the live filesystem view.
type LocalWorkspace interface {
	HasOpenWriter(id model.ObjectID) bool
	AdoptReplacement(ctx context.Context, keep, absorbed model.ObjectID) error
}

type Committer interface {
	Record(ctx context.Context, commitID, driveID string, ops []journal.Op) (journal.Commit, error)
}

type OutgoingDeps struct {
	Index     *index.Store
	Cache     *cache.Cache
	Workspace LocalWorkspace
	Remote    RemoteStore
	Journal   Committer
	Keyring   *crypto.Keyring
	Random    io.Reader
	Now       func() time.Time
	Logger    *slog.Logger
	// OnMovedOut is told once per item that left the vault, after it is safe in the trash.
	OnMovedOut func(path string, folder bool, files int)
}

type OutgoingConfig struct {
	VaultID       string
	Device        model.DeviceID
	TreeFolderID  string
	TrashFolderID string
	Readiness     ReadinessConfig
	Uploads       int
	PollInterval  time.Duration
	RetryBase     time.Duration
	RetryMax      time.Duration
}

type Outgoing struct {
	OutgoingDeps
	config   OutgoingConfig
	batchMu  sync.Mutex
	runMu    sync.Mutex
	failures int
}

func NewOutgoing(deps OutgoingDeps, config OutgoingConfig) *Outgoing {
	if config.Uploads < 1 {
		config.Uploads = 4
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.RetryBase == 0 {
		config.RetryBase, config.RetryMax = time.Second, 64*time.Second
	}
	return &Outgoing{OutgoingDeps: deps, config: config}
}

// Run replays any batch interrupted by a crash, then keeps syncing until ctx ends. A failed
// batch stays persisted and is retried with growing delays.
func (o *Outgoing) Run(ctx context.Context) error {
	for {
		delay := o.config.PollInterval
		if _, err := o.syncOnce(ctx, false); err != nil && ctx.Err() == nil {
			delay = o.retryDelay()
			o.Logger.Warn("sync batch failed", "err", err, "retryIn", delay)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// FlushNow syncs everything that is ready, ignoring quiet periods and delete holds.
func (o *Outgoing) FlushNow(ctx context.Context) error {
	for {
		worked, err := o.syncOnce(ctx, true)
		if err != nil || !worked {
			return err
		}
	}
}

// Drain flushes pending work before a lock, giving up after timeout.
func (o *Outgoing) Drain(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return o.FlushNow(ctx)
}

func (o *Outgoing) syncOnce(ctx context.Context, ignoreQuiet bool) (bool, error) {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	plans, err := o.loadOpenBatches(ctx)
	if err != nil {
		return false, err
	}
	if len(plans) == 0 {
		plan, err := o.formBatch(ctx, ignoreQuiet)
		if err != nil || plan == nil {
			return false, err
		}
		plans = append(plans, plan)
	}
	for _, plan := range plans {
		if err := o.runBatch(ctx, plan); err != nil {
			o.failures++
			return true, err
		}
	}
	o.failures = 0
	return true, nil
}

// Exclusive runs fn while no batch is in flight, so remote changes never interleave with
// an upload of the same object.
func (o *Outgoing) Exclusive(fn func() error) error {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	return fn()
}

func (o *Outgoing) retryDelay() time.Duration {
	delay := o.config.RetryBase << min(o.failures, 6)
	return min(delay, o.config.RetryMax)
}

// Backlog counts files whose latest content is not yet safely on Drive.
func (o *Outgoing) Backlog(ctx context.Context) (files int, bytes int64, err error) {
	err = o.Index.View(ctx, func(tx *index.Tx) error {
		dirty, err := tx.ObjectsInState(model.Dirty)
		for _, object := range dirty {
			if object.Kind == model.KindFile && !object.LocalOnly {
				files, bytes = files+1, bytes+object.Size
			}
		}
		return err
	})
	return files, bytes, err
}

func (o *Outgoing) runBatch(ctx context.Context, plan *BatchPlan) error {
	if !plan.Committed {
		if err := o.executeBatch(ctx, plan); err != nil {
			return err
		}
		if err := o.commitBatch(ctx, plan); err != nil {
			return err
		}
	}
	return o.finalizeBatch(ctx, plan)
}
