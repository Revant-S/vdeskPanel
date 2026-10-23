// Package orphans finds and removes Drive files under tree/ and trash/ that nothing in the
// log references any more, left behind by crashes and lost races (arbitration R15).
package orphans

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"vault/internal/model"
)

type Remote interface {
	ListChildren(ctx context.Context, folderID string) ([]model.RemoteFile, error)
	Delete(ctx context.Context, driveID string) error
}

type Deps struct {
	Remote Remote
	// References returns every Drive ID the index or an open batch points at.
	References func(ctx context.Context) (map[string]bool, error)
	Now        func() time.Time
}

type Config struct {
	VaultID       string
	TreeFolderID  string
	TrashFolderID string
	MinAge        time.Duration
	Listings      int
}

type Result struct {
	Found   int
	Deleted int
}

type Collector struct {
	deps   Deps
	config Config
}

func New(deps Deps, config Config) *Collector {
	if config.Listings <= 0 {
		config.Listings = 4
	}
	return &Collector{deps: deps, config: config}
}

// Collect deletes orphans older than MinAge. References are read again after the walk, so a
// file whose commit was applied while the walk ran is never taken for an orphan. A folder
// orphan is deleted whole, without walking into it.
func (c *Collector) Collect(ctx context.Context) (Result, error) {
	before, err := c.deps.References(ctx)
	if err != nil {
		return Result{}, err
	}
	candidates, err := c.walk(ctx, before)
	if err != nil {
		return Result{}, err
	}
	after, err := c.deps.References(ctx)
	if err != nil {
		return Result{}, err
	}
	var result Result
	cutoff := c.deps.Now().Add(-c.config.MinAge)
	for _, file := range candidates {
		if after[file.ID] {
			continue
		}
		result.Found++
		if file.CreatedTime.After(cutoff) {
			continue
		}
		if err := c.deps.Remote.Delete(ctx, file.ID); err != nil {
			return result, fmt.Errorf("delete orphan: %w", err)
		}
		result.Deleted++
	}
	return result, nil
}

func (c *Collector) walk(ctx context.Context, referenced map[string]bool) ([]model.RemoteFile, error) {
	trash, err := c.deps.Remote.ListChildren(ctx, c.config.TrashFolderID)
	if err != nil {
		return nil, fmt.Errorf("list trash: %w", err)
	}
	candidates := c.unreferenced(trash, referenced)
	level := []string{c.config.TreeFolderID}
	for len(level) > 0 {
		listings, err := c.list(ctx, level)
		if err != nil {
			return nil, err
		}
		level = nil
		for _, files := range listings {
			candidates = append(candidates, c.unreferenced(files, referenced)...)
			for _, file := range files {
				if file.Folder && referenced[file.ID] {
					level = append(level, file.ID)
				}
			}
		}
	}
	return candidates, nil
}

func (c *Collector) unreferenced(files []model.RemoteFile, referenced map[string]bool) []model.RemoteFile {
	var out []model.RemoteFile
	for _, file := range files {
		if file.Properties["vid"] == c.config.VaultID && !referenced[file.ID] {
			out = append(out, file)
		}
	}
	return out
}

func (c *Collector) list(ctx context.Context, folders []string) ([][]model.RemoteFile, error) {
	listings := make([][]model.RemoteFile, len(folders))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(c.config.Listings)
	for i, folder := range folders {
		group.Go(func() error {
			var err error
			if listings[i], err = c.deps.Remote.ListChildren(groupCtx, folder); err != nil {
				return fmt.Errorf("list folder: %w", err)
			}
			return nil
		})
	}
	return listings, group.Wait()
}
