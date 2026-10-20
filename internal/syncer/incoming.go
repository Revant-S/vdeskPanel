package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

var ErrChangesExpired = errors.New("syncer: changes page token expired")

const pageTokenKey = "changes/page-token"

type ChangeSource interface {
	StartPageToken(ctx context.Context) (string, error)
	Changes(ctx context.Context, token string) (model.ChangesPage, error)
	ListChildren(ctx context.Context, folderID string) ([]model.RemoteFile, error)
}

type CommitIngester interface {
	Ingest(ctx context.Context, files []model.RemoteFile, notFound func(error) bool) ([]journal.Commit, error)
	OpenRecord(record index.CommitRecord) (journal.Commit, error)
}

type RemoteApplier interface {
	ApplyRemoteCommit(ctx context.Context, commit journal.Commit) error
}

type IncomingDeps struct {
	Index     *index.Store
	Source    ChangeSource
	Journal   CommitIngester
	Applier   RemoteApplier
	Exclusive func(func() error) error
	NotFound  func(error) bool
	Now       func() time.Time
	Logger    *slog.Logger
}

type IncomingConfig struct {
	Device         model.DeviceID
	LogFolderID    string
	OwnLogFolderID string
	ActiveInterval time.Duration
	IdleInterval   time.Duration
	ActivityWindow time.Duration
}

type Incoming struct {
	IncomingDeps
	config       IncomingConfig
	mu           sync.Mutex
	deviceFolder map[string]bool
	lastActivity time.Time
}

func NewIncoming(deps IncomingDeps, config IncomingConfig) *Incoming {
	if config.ActiveInterval == 0 {
		config.ActiveInterval, config.IdleInterval, config.ActivityWindow = 10*time.Second, 30*time.Second, 5*time.Minute
	}
	return &Incoming{IncomingDeps: deps, config: config, deviceFolder: map[string]bool{}}
}

// NoteActivity switches polling to the faster interval for the activity window.
func (i *Incoming) NoteActivity() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastActivity = i.Now()
}

func (i *Incoming) Run(ctx context.Context) error {
	for {
		if err := i.PollOnce(ctx); err != nil && ctx.Err() == nil {
			i.Logger.Warn("poll remote changes", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(i.interval()):
		}
	}
}

func (i *Incoming) interval() time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.Now().Sub(i.lastActivity) < i.config.ActivityWindow {
		return i.config.ActiveInterval
	}
	return i.config.IdleInterval
}

// PollOnce reads the Drive changes feed, ingests new commits from other devices and applies
// them. The page token is saved only after the commits it covers are stored, so a crash
// re-reads changes rather than skipping them.
func (i *Incoming) PollOnce(ctx context.Context) error {
	token, err := i.savedToken(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return i.RebuildFromListing(ctx)
	}
	if err := i.learnDeviceFolders(ctx); err != nil {
		return err
	}
	folders, next, err := i.changedLogFolders(ctx, token)
	if errors.Is(err, ErrChangesExpired) {
		return i.RebuildFromListing(ctx)
	}
	if err != nil {
		return err
	}
	if err := i.ingestFolders(ctx, folders); err != nil {
		return err
	}
	if err := i.ApplyPending(ctx); err != nil {
		return err
	}
	return i.saveToken(ctx, next)
}

func (i *Incoming) changedLogFolders(ctx context.Context, token string) ([]string, string, error) {
	changed := map[string]bool{}
	for {
		page, err := i.Source.Changes(ctx, token)
		if err != nil {
			return nil, "", err
		}
		for _, change := range page.Changes {
			i.noteChangedFolder(change, changed)
		}
		if page.NextPageToken == "" {
			folders := make([]string, 0, len(changed))
			for folder := range changed {
				folders = append(folders, folder)
			}
			return folders, page.NewStartPageToken, nil
		}
		token = page.NextPageToken
	}
}

func (i *Incoming) noteChangedFolder(change model.RemoteChange, changed map[string]bool) {
	if change.File == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, parent := range change.File.Parents {
		switch {
		case parent == i.config.LogFolderID && change.File.Folder:
			i.deviceFolder[change.File.ID] = true
			changed[change.File.ID] = true
		case i.deviceFolder[parent] && parent != i.config.OwnLogFolderID:
			changed[parent] = true
		}
	}
}

// learnDeviceFolders lists the device log folders once per process, so changes inside them
// are recognised after a restart.
func (i *Incoming) learnDeviceFolders(ctx context.Context) error {
	i.mu.Lock()
	known := len(i.deviceFolder) > 0
	i.mu.Unlock()
	if known {
		return nil
	}
	_, err := i.listDeviceFolders(ctx)
	return err
}

func (i *Incoming) listDeviceFolders(ctx context.Context) ([]string, error) {
	devices, err := i.Source.ListChildren(ctx, i.config.LogFolderID)
	if err != nil {
		return nil, fmt.Errorf("list device logs: %w", err)
	}
	var folders []string
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, device := range devices {
		if device.Folder {
			i.deviceFolder[device.ID] = true
			folders = append(folders, device.ID)
		}
	}
	return folders, nil
}

// RebuildFromListing reads every device's log from scratch. It is how a new device joins and
// how a device recovers from an expired page token. The start token is taken first, so any
// change made during the listing is still seen by the next poll.
func (i *Incoming) RebuildFromListing(ctx context.Context) error {
	token, err := i.Source.StartPageToken(ctx)
	if err != nil {
		return fmt.Errorf("start page token: %w", err)
	}
	folders, err := i.listDeviceFolders(ctx)
	if err != nil {
		return err
	}
	if err := i.ingestFolders(ctx, folders); err != nil {
		return err
	}
	if err := i.ApplyPending(ctx); err != nil {
		return err
	}
	return i.saveToken(ctx, token)
}

func (i *Incoming) ingestFolders(ctx context.Context, folders []string) error {
	for _, folder := range folders {
		if folder == i.config.OwnLogFolderID {
			continue
		}
		if err := i.ingestFolder(ctx, folder); err != nil {
			return err
		}
	}
	return nil
}

// ingestFolder lists a device's log folder and ingests what is new. A file deleted between
// listing and download was packed by its device, so the folder is listed once more.
func (i *Incoming) ingestFolder(ctx context.Context, folder string) error {
	for attempt := 0; ; attempt++ {
		files, err := i.Source.ListChildren(ctx, folder)
		if err != nil {
			return fmt.Errorf("list log folder: %w", err)
		}
		_, err = i.Journal.Ingest(ctx, files, i.NotFound)
		if !errors.Is(err, journal.ErrLogFileGone) || attempt > 0 {
			return err
		}
	}
}

func (i *Incoming) savedToken(ctx context.Context) (string, error) {
	var token string
	err := i.Index.View(ctx, func(tx *index.Tx) error {
		value, _, err := tx.KV(pageTokenKey)
		token = string(value)
		return err
	})
	return token, err
}

func (i *Incoming) saveToken(ctx context.Context, token string) error {
	return i.Index.Update(ctx, func(tx *index.Tx) error { return tx.SetKV(pageTokenKey, []byte(token)) })
}
