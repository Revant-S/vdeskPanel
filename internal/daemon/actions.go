package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/config"
	"github.com/cosmoboy/vault/internal/devices"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/workspace"
)

func (d *Daemon) RestoreTrash(ctx context.Context, id string) (string, error) {
	s, err := d.currentSession()
	if err != nil {
		return "", err
	}
	restored, err := s.workspace.RestoreTrash(ctx, id)
	if errors.Is(err, workspace.ErrRestoreWhileLive) {
		return "", fmt.Errorf("%w: %v", api.ErrBusy, err)
	}
	return restored, notFoundAsAPI(err)
}

func (d *Daemon) PurgeTrash(ctx context.Context, id string) error {
	s, err := d.currentSession()
	if err != nil {
		return err
	}
	_, err = s.workspace.PurgeTrash(ctx, id)
	return notFoundAsAPI(err)
}

func (d *Daemon) EmptyTrash(ctx context.Context) (api.EmptyTrashResult, error) {
	items, err := d.Trash(ctx)
	if err != nil {
		return api.EmptyTrashResult{}, err
	}
	s, err := d.currentSession()
	if err != nil {
		return api.EmptyTrashResult{}, err
	}
	var result api.EmptyTrashResult
	for _, item := range items {
		size, err := s.workspace.PurgeTrash(ctx, item.ID)
		if err != nil {
			return result, err
		}
		result.Items, result.Bytes = result.Items+1, result.Bytes+size
	}
	return result, nil
}

func (d *Daemon) Download(ctx context.Context, vaultPath string) error {
	s, entry, err := d.resolve(vaultPath)
	if err != nil {
		return err
	}
	var ids []model.ObjectID
	for _, file := range s.workspace.FilesUnder(entry.ID) {
		ids = append(ids, file.ID)
	}
	return s.restorer.RequestLocal(ctx, ids)
}

func (d *Daemon) Free(ctx context.Context, vaultPath string) error {
	s, entry, err := d.resolve(vaultPath)
	if err != nil {
		return err
	}
	_, err = s.workspace.FreeLocal(ctx, entry.ID)
	if errors.Is(err, workspace.ErrNotBackedUp) {
		return fmt.Errorf("%w: %v", api.ErrBusy, err)
	}
	return err
}

func (d *Daemon) resolve(vaultPath string) (*session, model.Entry, error) {
	s, err := d.currentSession()
	if err != nil {
		return nil, model.Entry{}, err
	}
	entry, ok := s.workspace.Tree().Resolve(cleanVaultPath(vaultPath))
	if !ok {
		return nil, model.Entry{}, api.ErrNotFound
	}
	return s, entry, nil
}

func (d *Daemon) SyncNow(ctx context.Context) error {
	s, err := d.currentSession()
	if err != nil {
		return err
	}
	if err := s.outgoing.FlushNow(ctx); err != nil {
		return err
	}
	return s.incoming.PollOnce(ctx)
}

func (d *Daemon) Verify(ctx context.Context, deep bool) (api.VerifyReport, error) {
	s, err := d.currentSession()
	if err != nil {
		return api.VerifyReport{}, err
	}
	report, err := s.verifier.Verify(ctx, deep)
	out := api.VerifyReport{Deep: deep, FilesChecked: report.FilesChecked}
	for _, problem := range report.Problems {
		out.Problems = append(out.Problems, api.VerifyProblem{Path: problem.Path, Kind: string(problem.Kind), Trashed: problem.Trashed})
	}
	return out, err
}

func (d *Daemon) PauseRestore(ctx context.Context, paused bool) error {
	s, err := d.currentSession()
	if err != nil {
		return err
	}
	if paused {
		return s.restorer.Pause(ctx)
	}
	return s.restorer.Resume(ctx)
}

func (d *Daemon) Settings(context.Context) (api.Settings, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.config
	return api.Settings{MountPoint: c.MountPoint, DeviceName: c.DeviceName, Uploads: c.Workers.Uploads, Downloads: c.Workers.Downloads,
		ActivePollSeconds: int(c.Polling.Active.Seconds()), IdlePollSeconds: int(c.Polling.Idle.Seconds()),
		TrashRetentionDays: c.TrashRetentionDays, ThumbnailerDenyList: c.ThumbnailerDenyList}, nil
}

// UpdateSettings saves new settings. Most take effect at the next unlock.
func (d *Daemon) UpdateSettings(_ context.Context, settings api.Settings) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.config
	c.MountPoint, c.DeviceName, c.TrashRetentionDays, c.ThumbnailerDenyList = settings.MountPoint, settings.DeviceName, settings.TrashRetentionDays, settings.ThumbnailerDenyList
	c.Workers.Uploads, c.Workers.Downloads = settings.Uploads, settings.Downloads
	c.Polling.Active.Duration = time.Duration(settings.ActivePollSeconds) * time.Second
	c.Polling.Idle.Duration = time.Duration(settings.IdlePollSeconds) * time.Second
	if err := c.Validate(); err != nil {
		return fmt.Errorf("%w: %v", api.ErrInvalidRequest, err)
	}
	if err := config.Save(d.Layout.ConfigFile, c); err != nil {
		return err
	}
	d.config = c
	return nil
}

func (d *Daemon) RetireDevice(ctx context.Context, deviceID string) error {
	s, err := d.currentSession()
	if err != nil {
		return err
	}
	return s.devices.Publish(ctx, model.DeviceID(deviceID), func(r *devices.Record) { r.Retired = true })
}

func notFoundAsAPI(err error) error {
	if err != nil && (errors.Is(err, index.ErrNotFound) || errors.Is(err, fs.ErrNotExist)) {
		return fmt.Errorf("%w: %v", api.ErrNotFound, err)
	}
	return err
}
