package daemon

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

func (s *session) fillStatus(ctx context.Context, status *api.Status) {
	status.Online = s.online()
	status.NotBackedUpFiles, status.NotBackedUpBytes, _ = s.outgoing.Backlog(ctx)
	s.index.View(ctx, func(tx *index.Tx) error {
		pending, _ := tx.CountPendingOps()
		corrupt, _ := tx.ObjectsInState(model.Corrupt)
		status.SyncingItems, status.CorruptFiles = pending, len(corrupt)
		if commit, err := tx.LatestCommitOf(s.daemon.device); err == nil {
			status.LastBackedUp = time.UnixMilli(commit.Clock.WallMillis)
		}
		return nil
	})
	if progress := s.restorer.Progress(); !progress.Complete {
		status.Restore = &api.RestoreProgress{FilesDone: progress.FilesDone, FilesTotal: progress.FilesTotal,
			BytesDone: progress.BytesDone, BytesTotal: progress.BytesTotal, Paused: progress.Paused}
		status.State = api.StateRestoring
	}
}

func (d *Daemon) Tree(_ context.Context, folderPath string) ([]api.TreeEntry, error) {
	s, err := d.currentSession()
	if err != nil {
		return nil, err
	}
	tree := s.workspace.Tree()
	folder, ok := tree.Resolve(cleanVaultPath(folderPath))
	if !ok {
		return nil, api.ErrNotFound
	}
	var out []api.TreeEntry
	for _, child := range tree.Children(folder.ID) {
		total, local := tree.Aggregate(child.ID)
		out = append(out, api.TreeEntry{Name: child.Name, Path: path.Join(cleanVaultPath(folderPath), child.Name), Folder: child.IsFolder(),
			Size: total, LocalSize: local, State: fileState(child.State), ModTime: child.ModTime})
	}
	return out, nil
}

func fileState(state model.LocalState) api.FileState {
	switch state {
	case model.CloudOnly:
		return api.FileCloudOnly
	case model.Partial:
		return api.FilePartial
	case model.Dirty:
		return api.FileDirty
	case model.Corrupt:
		return api.FileCorrupt
	}
	return api.FileLocal
}

func cleanVaultPath(p string) string {
	return path.Clean("/" + p)
}

// Commits pages the history newest first. A path filter keeps only commits that touched
// that path or anything under it.
func (d *Daemon) Commits(ctx context.Context, before string, limit int, filter string) (api.CommitPage, error) {
	s, err := d.currentSession()
	if err != nil {
		return api.CommitPage{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	cursor, err := model.ParseStamp(before)
	if err != nil {
		return api.CommitPage{}, fmt.Errorf("%w: %v", api.ErrInvalidRequest, err)
	}
	page := api.CommitPage{}
	for len(page.Commits) < limit {
		records, err := s.commitsBefore(ctx, cursor, limit)
		if err != nil {
			return page, err
		}
		if len(records) == 0 {
			page.NextBefore = ""
			break
		}
		for _, record := range records {
			cursor = record.Clock
			commit, err := s.journal.OpenRecord(record)
			if err != nil {
				return page, err
			}
			if view := commitView(commit); filter == "" || touches(view, cleanVaultPath(filter)) {
				page.Commits = append(page.Commits, view)
			}
		}
		page.NextBefore = cursor.String()
	}
	page.Devices, err = d.Devices(ctx)
	return page, err
}

func (s *session) commitsBefore(ctx context.Context, cursor model.Stamp, limit int) ([]index.CommitRecord, error) {
	var records []index.CommitRecord
	err := s.index.View(ctx, func(tx *index.Tx) error {
		var err error
		records, err = tx.CommitsBefore(cursor, limit)
		return err
	})
	return records, err
}

func commitView(commit journal.Commit) api.CommitView {
	view := api.CommitView{ID: commit.ID, Device: string(commit.Device), DeviceName: commit.DeviceName, Clock: commit.Clock.String(),
		Time: time.UnixMilli(commit.Clock.WallMillis), Parents: commit.Parents}
	counts := map[string]int{}
	for _, op := range commit.Ops {
		view.Ops = append(view.Ops, api.OpView{Kind: string(op.Kind), Path: op.Path, From: op.From, To: op.To})
		counts[string(op.Kind)]++
	}
	view.Summary = summarize(counts)
	return view
}

func summarize(counts map[string]int) string {
	labels := map[string]string{"create": "created", "modify": "modified", "rename": "renamed", "move": "moved", "delete": "deleted",
		"moved_out": "moved out", "superseded": "superseded", "trash_restore": "restored", "trash_purge": "purged"}
	var parts []string
	for _, kind := range []string{"create", "modify", "rename", "move", "delete", "moved_out", "superseded", "trash_restore", "trash_purge"} {
		if counts[kind] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[kind], labels[kind]))
		}
	}
	return strings.Join(parts, ", ")
}

func touches(view api.CommitView, filter string) bool {
	for _, op := range view.Ops {
		for _, p := range []string{op.Path, op.From, op.To} {
			if p != "" && (p == filter || strings.HasPrefix(p, strings.TrimSuffix(filter, "/")+"/")) {
				return true
			}
		}
	}
	return false
}

// Devices lists every device with a record on Drive or a commit in the log. A record's name
// and first-seen time win over the log's, since the record is updated on rename.
func (d *Daemon) Devices(ctx context.Context) ([]api.DeviceView, error) {
	s, err := d.currentSession()
	if err != nil {
		return nil, err
	}
	byID := map[string]*api.DeviceView{}
	err = s.index.View(ctx, func(tx *index.Tx) error {
		records, err := tx.CommitsBefore(model.Stamp{}, 100000)
		for i := len(records) - 1; i >= 0; i-- {
			commit, err := s.journal.OpenRecord(records[i])
			if err != nil {
				return err
			}
			device := byID[string(commit.Device)]
			if device == nil {
				device = &api.DeviceView{ID: string(commit.Device), FirstSeen: time.UnixMilli(commit.Clock.WallMillis)}
				byID[device.ID] = device
			}
			device.Name = commit.DeviceName
		}
		return err
	})
	if byID[string(d.device)] == nil {
		byID[string(d.device)] = &api.DeviceView{ID: string(d.device), Name: d.config.DeviceName, FirstSeen: d.Now()}
	}
	for id, record := range s.devices.Cached() {
		device := byID[string(id)]
		if device == nil {
			device = &api.DeviceView{ID: string(id)}
			byID[device.ID] = device
		}
		device.Name, device.FirstSeen, device.Retired = record.Name, record.FirstSeen, record.Retired
	}
	var out []api.DeviceView
	for _, device := range byID {
		device.This = device.ID == string(d.device)
		out = append(out, *device)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].FirstSeen.Before(out[b].FirstSeen) })
	return out, err
}

func (d *Daemon) Trash(ctx context.Context) ([]api.TrashItem, error) {
	s, err := d.currentSession()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	devices, _ := d.Devices(ctx)
	for _, device := range devices {
		names[device.ID] = device.Name
	}
	retention := time.Duration(d.config.TrashRetentionDays) * 24 * time.Hour
	var items []api.TrashItem
	err = s.index.View(ctx, func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		for _, r := range records {
			items = append(items, api.TrashItem{ID: r.ID, Name: path.Base(r.OriginalPath), OriginalPath: r.OriginalPath, Reason: r.Reason,
				Device: string(r.Device), DeviceName: names[string(r.Device)], DeletedAt: r.DeletedAt, ExpiresAt: r.DeletedAt.Add(retention),
				Size: r.Size, FileCount: r.FileCount, Folder: r.Folder})
		}
		return err
	})
	return items, err
}
