package daemon

import (
	"context"
	"strconv"
	"time"

	"vault/internal/index"
	"vault/internal/orphans"
)

const (
	orphanMinAge     = 30 * 24 * time.Hour
	orphanGCInterval = 24 * time.Hour
	orphanGCCheck    = time.Hour
	lastOrphanGCKey  = "orphans/last_collected"
)

func (s *session) collectOrphansHourly(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(orphanGCCheck):
		}
		s.collectOrphansIfDue(ctx)
	}
}

// collectOrphansIfDue runs at most once a day across restarts, since it lists every folder
// on Drive. It needs a recent poll that applied the whole log: on an index that is behind,
// every file another device made would look orphaned.
func (s *session) collectOrphansIfDue(ctx context.Context) {
	now := s.daemon.Now()
	if last := s.lastOrphanCollection(ctx); now.Sub(last) < orphanGCInterval || !s.online() || !s.caughtUp() {
		return
	}
	collector := orphans.New(orphans.Deps{Remote: s.client, References: s.referencedDriveIDs, Now: s.daemon.Now},
		orphans.Config{VaultID: s.vaultID, TreeFolderID: s.folders.Tree, TrashFolderID: s.folders.Trash, MinAge: orphanMinAge})
	result, err := collector.Collect(ctx)
	if err != nil {
		s.daemon.Logger.Warn("collect orphans", "err", err)
		return
	}
	if result.Refused {
		s.daemon.Logger.Warn("orphan collection refused: too many unreferenced files", "listed", result.Listed, "found", result.Found)
		return
	}
	s.daemon.Logger.Info("collected orphans", "listed", result.Listed, "found", result.Found, "deleted", result.Deleted)
	s.index.Update(ctx, func(tx *index.Tx) error {
		return tx.SetKV(lastOrphanGCKey, []byte(strconv.FormatInt(now.Unix(), 10)))
	})
}

func (s *session) caughtUp() bool {
	at := s.incoming.CaughtUpAt()
	return !at.IsZero() && s.daemon.Now().Sub(at) < 2*s.daemon.config.Polling.Idle.Duration+time.Minute
}

func (s *session) lastOrphanCollection(ctx context.Context) time.Time {
	var last time.Time
	s.index.View(ctx, func(tx *index.Tx) error {
		if data, ok, err := tx.KV(lastOrphanGCKey); err == nil && ok {
			if seconds, err := strconv.ParseInt(string(data), 10, 64); err == nil {
				last = time.Unix(seconds, 0)
			}
		}
		return nil
	})
	return last
}

func (s *session) referencedDriveIDs(ctx context.Context) (map[string]bool, error) {
	var ids map[string]bool
	err := s.index.View(ctx, func(tx *index.Tx) error {
		var err error
		ids, err = tx.ReferencedDriveIDs()
		return err
	})
	if err != nil {
		return nil, err
	}
	batched, err := s.outgoing.BatchDriveIDs(ctx)
	for id := range batched {
		ids[id] = true
	}
	return ids, err
}
