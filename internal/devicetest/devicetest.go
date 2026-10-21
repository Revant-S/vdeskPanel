// Package devicetest runs several simulated devices of one vault against one fake Drive, so
// multi-device behaviour can be tested in one process.
package devicetest

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/drive/drivefake"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/remote"
	"github.com/cosmoboy/vault/internal/syncer"
	"github.com/cosmoboy/vault/internal/vaultlayout"
	"github.com/cosmoboy/vault/internal/workspace"
)

type Network struct {
	Drive   *drivefake.Server
	Keyring *crypto.Keyring
	VaultID string
	Folders vaultlayout.Folders
	Logger  *slog.Logger
}

func NewNetwork(t *testing.T, options ...drivefake.Option) *Network {
	t.Helper()
	_, keyring, err := crypto.CreateVault([]byte("correct horse battery"), crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keyring.Destroy)
	var vaultID string
	keyring.Use(func(k *crypto.Keys) error { vaultID = hex.EncodeToString(k.VaultID[:]); return nil })
	network := &Network{Drive: drivefake.New(options...), Keyring: keyring, VaultID: vaultID,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	network.Folders, err = vaultlayout.EnsureFolders(context.Background(), network.Client(t, FastDrive()))
	if err != nil {
		t.Fatal(err)
	}
	return network
}

// FastDrive keeps the real budget shape but retries in milliseconds, so tests stay quick.
func FastDrive() drive.Config {
	config := drive.DefaultConfig()
	config.Budget = drive.BudgetConfig{MetadataWritesPerSecond: 1000, MetadataWriteBurst: 1000, ReadsPerSecond: 1000, ReadBurst: 1000}
	config.Retry = drive.RetryConfig{BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, MaxAttempts: 8}
	config.ChunkSize = 256 << 10
	return config
}

func (n *Network) Client(t *testing.T, config drive.Config) *drive.Client {
	t.Helper()
	config.Endpoint, config.UploadEndpoint = n.Drive.Endpoint(), n.Drive.UploadEndpoint()
	client, err := drive.NewClient(context.Background(), config, drive.Deps{HTTPClient: n.Drive.HTTPClient(), Logger: n.Logger, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type Device struct {
	Network   *Network
	ID        model.DeviceID
	Home      string
	Config    drive.Config
	Client    *drive.Client
	Index     *index.Store
	Cache     *cache.Cache
	Workspace *workspace.Workspace
	Journal   *journal.Journal
	Outgoing  *syncer.Outgoing
	Incoming  *syncer.Incoming
	LogFolder string
	t         *testing.T
	codec     *journal.Codec
}

// NewDevice starts a device with its own home directory, as a separate laptop would have.
func (n *Network) NewDevice(t *testing.T, name string) *Device {
	t.Helper()
	device := &Device{Network: n, ID: model.DeviceID("d-" + name), Home: t.TempDir(), Config: FastDrive(), t: t}
	device.Start()
	return device
}

func (d *Device) Start() {
	t := d.t
	t.Helper()
	ctx := context.Background()
	n := d.Network
	d.Client = n.Client(t, d.Config)
	var err error
	d.Index, err = index.Open(ctx, filepath.Join(d.Home, "index.db"), crypto.NewIndexSealer(n.Keyring, rand.Reader))
	if err != nil {
		t.Fatal(err)
	}
	content := func(use func(cipher.AEAD) error) error {
		return n.Keyring.Use(func(k *crypto.Keys) error { return use(k.Content) })
	}
	d.Cache, err = cache.New(filepath.Join(d.Home, "cache"), cache.Deps{Fetcher: &remote.Bodies{Client: d.Client, Index: d.Index},
		Bitmaps: d.Index, Content: content, Random: rand.Reader, Logger: n.Logger}, cache.Config{})
	if err != nil {
		t.Fatal(err)
	}
	clock := journal.NewClock(d.ID, time.Now, model.Stamp{}, nil, n.Logger)
	d.Workspace, err = workspace.Open(ctx, workspace.Deps{Index: d.Index, Cache: d.Cache, Names: crypto.NewKeyringNames(n.Keyring, rand.Reader),
		Clock: clock, Random: rand.Reader, Now: time.Now, Logger: n.Logger}, workspace.Config{UID: 1000, Device: d.ID, DeviceName: string(d.ID)})
	if err != nil {
		t.Fatal(err)
	}
	d.startSync(clock)
	t.Cleanup(d.Stop)
}

func (d *Device) startSync(clock *journal.Clock) {
	t, n := d.t, d.Network
	var err error
	d.LogFolder, err = vaultlayout.EnsureDeviceLogFolder(context.Background(), d.Client, n.Folders, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.codec, err = journal.NewCodec(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d.Journal = journal.New(journal.Deps{Remote: &remote.Log{Client: d.Client}, Store: d.Index, Keyring: n.Keyring, Codec: d.codec,
		Clock: clock, Logger: n.Logger}, journal.Config{VaultID: n.VaultID, Device: d.ID, DeviceName: string(d.ID), OwnLogFolderID: d.LogFolder})
	d.Outgoing = syncer.NewOutgoing(syncer.OutgoingDeps{Index: d.Index, Cache: d.Cache, Workspace: d.Workspace,
		Remote: &remote.Tree{Client: d.Client}, Journal: d.Journal, Keyring: n.Keyring, Random: rand.Reader, Now: time.Now, Logger: n.Logger},
		syncer.OutgoingConfig{VaultID: n.VaultID, Device: d.ID, TreeFolderID: n.Folders.Tree, TrashFolderID: n.Folders.Trash,
			Readiness: syncer.ReadinessConfig{MaxBatch: 200}, RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond})
	d.Incoming = syncer.NewIncoming(syncer.IncomingDeps{Index: d.Index, Source: &remote.Changes{Client: d.Client}, Journal: d.Journal,
		Applier: d.Workspace, Exclusive: d.Outgoing.Exclusive, NotFound: remote.IsNotFound, Now: time.Now, Logger: n.Logger},
		syncer.IncomingConfig{Device: d.ID, LogFolderID: n.Folders.Log, OwnLogFolderID: d.LogFolder})
}

// Stop closes the device without flushing anything, like a process that was killed.
func (d *Device) Stop() {
	if d.Cache == nil {
		return
	}
	d.Workspace.Freeze()
	d.Cache.Close()
	d.Index.Close()
	d.codec.Close()
	d.Cache = nil
}

// Restart reopens the same home directory, as after a crash or reboot.
func (d *Device) Restart() {
	d.Stop()
	d.Start()
}
