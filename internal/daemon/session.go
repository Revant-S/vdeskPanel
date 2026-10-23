package daemon

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sync/errgroup"
	"vault/internal/cache"
	"vault/internal/crypto"
	"vault/internal/devices"
	"vault/internal/drive"
	"vault/internal/fusefs"
	"vault/internal/index"
	"vault/internal/journal"
	"vault/internal/model"
	"vault/internal/remote"
	"vault/internal/restore"
	"vault/internal/syncer"
	"vault/internal/vaultlayout"
	"vault/internal/verify"
	"vault/internal/workspace"
)

// session is everything that exists only while the vault is unlocked.
type session struct {
	daemon    *Daemon
	vaultID   string
	keyring   *crypto.Keyring
	client    *drive.Client
	folders   vaultlayout.Folders
	index     *index.Store
	cache     *cache.Cache
	codec     *journal.Codec
	devices   *devices.Registry
	workspace *workspace.Workspace
	journal   *journal.Journal
	outgoing  *syncer.Outgoing
	incoming  *syncer.Incoming
	restorer  *restore.Restorer
	verifier  *verify.Verifier
	mount     *fusefs.Mount
	cancel    context.CancelFunc
	group     *errgroup.Group
}

func vaultIDOf(keyring *crypto.Keyring) string {
	var id string
	keyring.Use(func(k *crypto.Keys) error { id = hex.EncodeToString(k.VaultID[:]); return nil })
	return id
}

// openSession builds the unlocked vault and mounts it. On failure everything opened so far
// is closed again.
func (d *Daemon) openSession(ctx context.Context, keyring *crypto.Keyring, client *drive.Client, restoreMode restore.Mode) (*session, error) {
	s := &session{daemon: d, vaultID: vaultIDOf(keyring), keyring: keyring, client: client}
	if err := s.openStorage(ctx); err != nil {
		s.closeStorage()
		return nil, err
	}
	if err := s.openSync(ctx, restoreMode); err != nil {
		s.closeStorage()
		return nil, err
	}
	s.catchUp(ctx)
	if err := s.mountVault(); err != nil {
		s.closeStorage()
		return nil, err
	}
	s.start()
	return s, nil
}

func (s *session) openStorage(ctx context.Context) error {
	layout, logger := s.daemon.Layout, s.daemon.Logger
	var err error
	if s.folders, err = vaultlayout.EnsureFolders(ctx, s.client); err != nil {
		return err
	}
	if s.index, err = index.Open(ctx, layout.IndexFile, crypto.NewIndexSealer(s.keyring, rand.Reader)); err != nil {
		return err
	}
	content := func(use func(cipher.AEAD) error) error {
		return s.keyring.Use(func(k *crypto.Keys) error { return use(k.Content) })
	}
	s.cache, err = cache.New(layout.CacheDir, cache.Deps{Fetcher: &remote.Bodies{Client: s.client, Index: s.index}, Bitmaps: s.index, Content: content,
		Random: rand.Reader, Logger: logger}, cache.Config{})
	if err != nil {
		return err
	}
	s.codec, err = journal.NewCodec(rand.Reader)
	return err
}

func (s *session) mountVault() error {
	mountPoint := s.daemon.config.MountPoint
	if err := os.MkdirAll(mountPoint, 0o700); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}
	mount, err := fusefs.MountStore(s.workspace, fusefs.Options{MountPoint: mountPoint, UID: uint32(os.Getuid()), GID: uint32(os.Getgid()),
		EntryTimeout: time.Second, AttrTimeout: time.Second}, s.daemon.Logger)
	if err != nil {
		return err
	}
	s.mount = mount
	s.workspace.AttachInvalidator(mount)
	return nil
}

// catchUp applies remote changes before mounting, but never holds the mount back for long:
// offline, the vault still mounts and catches up later.
func (s *session) catchUp(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.daemon.config.UnlockCatchUpTimeout.Duration)
	defer cancel()
	if err := s.incoming.PollOnce(ctx); err != nil {
		s.daemon.Logger.Warn("catch up at unlock", "err", err)
	}
}

func (s *session) start() {
	ctx, cancel := context.WithCancel(context.Background())
	group, ctx := errgroup.WithContext(ctx)
	s.cancel, s.group = cancel, group
	group.Go(func() error { return s.outgoing.Run(ctx) })
	group.Go(func() error { return s.incoming.Run(ctx) })
	group.Go(func() error { return s.restorer.Run(ctx) })
	group.Go(func() error { return s.workspace.RunInvalidations(ctx) })
	group.Go(func() error { return s.expireTrashDaily(ctx) })
	group.Go(func() error { s.publishDevice(ctx); return nil })
	group.Go(func() error { return s.watchBackup(ctx) })
}

// publishDevice writes this device's record. Offline it is retried at the next unlock; the
// device list falls back to the log meanwhile.
func (s *session) publishDevice(ctx context.Context) {
	name := s.daemon.config.DeviceName
	if err := s.devices.Publish(ctx, s.daemon.device, func(r *devices.Record) { r.Name = name }); err != nil {
		s.daemon.Logger.Warn("publish device record", "err", err)
	}
}

func (s *session) expireTrashDaily(ctx context.Context) error {
	retention := time.Duration(s.daemon.config.TrashRetentionDays) * 24 * time.Hour
	for {
		if expired, err := s.workspace.ExpireTrash(ctx, retention); err != nil {
			s.daemon.Logger.Warn("expire trash", "err", err)
		} else if expired > 0 {
			s.daemon.Logger.Info("expired trash items", "count", expired)
		}
		s.collectOrphansIfDue(ctx)
		if _, err := s.devices.Refresh(ctx); err != nil {
			s.daemon.Logger.Debug("refresh device records", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(24 * time.Hour):
		}
	}
}

// close follows the lock order: unmount (lazily if files are held open), flush what is
// pending, stop every worker, close storage, and only then destroy the keys.
func (s *session) close(ctx context.Context) {
	logger := s.daemon.Logger
	if s.mount != nil {
		if err := s.mount.Unmount(); err != nil {
			s.workspace.Freeze()
			if err := s.mount.UnmountLazy(); err != nil {
				logger.Error("lazy unmount", "err", err)
			}
		}
	}
	if err := s.outgoing.Drain(ctx, s.daemon.config.LockFlushTimeout.Duration); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("flush before lock", "err", err)
	}
	if s.cancel != nil {
		s.cancel()
		s.group.Wait()
	}
	s.closeStorage()
	s.keyring.Destroy()
}

func (s *session) closeStorage() {
	if s.cache != nil {
		s.cache.Close()
	}
	if s.index != nil {
		s.index.Close()
	}
	if s.codec != nil {
		s.codec.Close()
	}
}

// lastStamp lets the clock resume after a restart without ever issuing a stamp older than
// one already used, even if the wall clock moved back.
func lastStamp(ctx context.Context, store *index.Store, device model.DeviceID) model.Stamp {
	var last model.Stamp
	store.View(ctx, func(tx *index.Tx) error {
		if commit, err := tx.LatestCommitOf(device); err == nil {
			last = commit.Clock
		}
		ops, err := tx.UnbatchedOps()
		for _, op := range ops {
			if op.Stamp.Device == device && op.Stamp.After(last) {
				last = op.Stamp
			}
		}
		return err
	})
	return last
}
