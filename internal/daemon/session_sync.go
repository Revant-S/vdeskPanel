package daemon

import (
	"context"
	"crypto/rand"
	"os"
	"path"
	"time"

	"vault/internal/crypto"
	"vault/internal/devices"
	"vault/internal/journal"
	"vault/internal/model"
	"vault/internal/notify"
	"vault/internal/procfilter"
	"vault/internal/remote"
	"vault/internal/restore"
	"vault/internal/syncer"
	"vault/internal/trash"
	"vault/internal/vaultlayout"
	"vault/internal/verify"
	"vault/internal/workspace"
)

const moveOutWindow = 10 * time.Second

func (s *session) openSync(ctx context.Context, restoreMode restore.Mode) error {
	d := s.daemon
	cfg, logger := d.config, d.Logger
	logFolder, err := vaultlayout.EnsureDeviceLogFolder(ctx, s.client, s.folders, d.device)
	if err != nil {
		return err
	}
	clock := journal.NewClock(d.device, d.Now, lastStamp(ctx, s.index, d.device), nil, logger)
	proc := procfilter.SystemProc()
	detector := trash.NewMoveOutDetector(proc, moveOutWindow, d.Now)
	denied := procfilter.New(proc, cfg.ThumbnailerDenyList)
	s.workspace, err = workspace.Open(ctx, workspace.Deps{Index: s.index, Cache: s.cache, Names: crypto.NewKeyringNames(s.keyring, rand.Reader),
		Clock: clock, Random: rand.Reader, Now: d.Now, Logger: logger, DenyRead: denied.Denies, Classify: detector, Reads: detector,
		DiskFree: workspace.StatFSOf(d.Layout.CacheDir), WantLocal: s.wantLocal},
		workspace.Config{Device: d.device, DeviceName: cfg.DeviceName, UID: uint32(os.Getuid()),
			QuietPeriod: cfg.QuietPeriod.Duration, DeleteHold: cfg.DeleteHold.Duration})
	if err != nil {
		return err
	}
	s.journal = journal.New(journal.Deps{Remote: &remote.Log{Client: s.client}, Store: s.index, Keyring: s.keyring, Codec: s.codec,
		Clock: clock, Logger: logger}, journal.Config{VaultID: s.vaultID, Device: d.device, DeviceName: cfg.DeviceName, OwnLogFolderID: logFolder})
	s.outgoing = syncer.NewOutgoing(syncer.OutgoingDeps{Index: s.index, Cache: s.cache, Workspace: s.workspace,
		Remote: &remote.Tree{Client: s.client}, Journal: s.journal, Keyring: s.keyring, Random: rand.Reader, Now: d.Now,
		Logger: logger, OnMovedOut: s.announceMovedOut},
		syncer.OutgoingConfig{VaultID: s.vaultID, Device: d.device, TreeFolderID: s.folders.Tree, TrashFolderID: s.folders.Trash,
			Uploads: cfg.Workers.Uploads, Readiness: syncer.ReadinessConfig{QuietPeriod: cfg.QuietPeriod.Duration,
				DeleteHold: cfg.DeleteHold.Duration, MaxDeleteHold: cfg.MaxDeleteHold.Duration, MaxBatch: 200}})
	s.incoming = syncer.NewIncoming(syncer.IncomingDeps{Index: s.index, Source: &remote.Changes{Client: s.client}, Journal: s.journal,
		Applier: s.workspace, Exclusive: s.outgoing.Exclusive, NotFound: remote.IsNotFound, Now: d.Now, Logger: logger},
		syncer.IncomingConfig{Device: d.device, LogFolderID: s.folders.Log, OwnLogFolderID: logFolder,
			ActiveInterval: cfg.Polling.Active.Duration, IdleInterval: cfg.Polling.Idle.Duration, ActivityWindow: cfg.Polling.ActivityWindow.Duration})
	s.devices = devices.New(devices.Deps{Remote: &remote.Devices{Log: remote.Log{Client: s.client}}, Keyring: s.keyring,
		Random: rand.Reader, Now: d.Now, Logger: logger}, devices.Config{VaultID: s.vaultID, FolderID: s.folders.Devices})
	s.restorer, err = restore.New(ctx, restore.Deps{Index: s.index, Cache: s.cache, UploadsPending: s.uploadsPending,
		OnLocal: s.workspace.NoteLocal, Logger: logger},
		restore.Config{Mode: restoreMode, Files: cfg.Workers.Downloads})
	if err != nil {
		return err
	}
	s.verifier = verify.New(verify.Deps{Index: s.index, Remote: &remote.Verify{Client: s.client}, Names: s.workspace.Names,
		Keyring: s.keyring, Logger: logger}, verify.Config{TreeFolderID: s.folders.Tree, TrashFolderID: s.folders.Trash, TempDir: d.Layout.CacheDir})
	return nil
}

func (s *session) wantLocal(id model.ObjectID) {
	if s.restorer != nil {
		s.restorer.Want(context.Background(), id)
	}
}

func (s *session) uploadsPending() bool {
	files, _, err := s.outgoing.Backlog(context.Background())
	return err == nil && files > 0
}

func (s *session) announceMovedOut(itemPath string, folder bool, files int) {
	notifier := s.daemon.Notifier
	if notifier == nil {
		return
	}
	retention := s.daemon.config.TrashRetentionDays
	summary, body := notify.FileMovedOut(path.Base(itemPath), retention)
	if folder {
		summary, body = notify.FolderMovedOut(path.Base(itemPath), files, retention)
	}
	if err := notifier.Notify(context.Background(), summary, body); err != nil {
		s.daemon.Logger.Debug("desktop notification", "err", err)
	}
}
