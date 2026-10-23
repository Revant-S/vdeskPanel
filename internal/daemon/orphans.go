package daemon

import (
	"context"
	"strconv"
	"time"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/orphans"
)

const (
	orphanMinAge     = 30 * 24 * time.Hour
	orphanGCInterval = 24 * time.Hour
	lastOrphanGCKey  = "orphans/last_collected"
)

// collectOrphansIfDue runs at most once a day across restarts, since it lists every folder
// on Drive.
func (s *session) collectOrphansIfDue(ctx context.Context) {
	now := s.daemon.Now()
	if last := s.lastOrphanCollection(ctx); now.Sub(last) < orphanGCInterval || !s.online() {
		return
	}
	collector := orphans.New(orphans.Deps{Remote: s.client, References: s.referencedDriveIDs, Now: s.daemon.Now},
		orphans.Config{VaultID: s.vaultID, TreeFolderID: s.folders.Tree, TrashFolderID: s.folders.Trash, MinAge: orphanMinAge})
	result, err := collector.Collect(ctx)
	if err != nil {
		s.daemon.Logger.Warn("collect orphans", "err", err)
		return
	}
	s.daemon.Logger.Info("collected orphans", "found", result.Found, "deleted", result.Deleted)
	s.index.Update(ctx, func(tx *index.Tx) error {
		return tx.SetKV(lastOrphanGCKey, []byte(strconv.FormatInt(now.Unix(), 10)))
	})
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
