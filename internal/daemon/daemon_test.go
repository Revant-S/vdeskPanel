package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"vault/internal/api"
	"vault/internal/config"
	"vault/internal/devicetest"
	"vault/internal/drive/drivefake"
	"vault/internal/orphans"
)

func newTestDaemon(t *testing.T, fake *drivefake.Server, name string) *Daemon {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("needs /dev/fuse")
	}
	home := t.TempDir()
	layout, err := config.HomeLayout(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(home)
	cfg.MountPoint, cfg.DeviceName = filepath.Join(t.TempDir(), "Vault"), name
	cfg.QuietPeriod.Duration, cfg.DeleteHold.Duration = 50*time.Millisecond, 50*time.Millisecond
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	connector := &EndpointConnector{Endpoint: fake.Endpoint(), UploadEndpoint: fake.UploadEndpoint(), HTTPClient: fake.HTTPClient(),
		Config: devicetest.FastDrive(), Logger: logger}
	d, err := New(Deps{Layout: layout, Drive: connector, Now: time.Now, Logger: logger}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Lock(context.Background()) })
	return d
}

func signIn(t *testing.T, d *Daemon) {
	t.Helper()
	if _, err := d.BeginSignIn(context.Background()); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if status, _ := d.Status(context.Background()); status.State == api.StateNeedsSetup {
			return
		}
	}
	t.Fatal("sign-in never completed")
}

func TestCreateLockUnlockAndJoin(t *testing.T) {
	ctx := context.Background()
	fake := drivefake.New()
	a := newTestDaemon(t, fake, "amber")
	signIn(t, a)
	if err := a.CreateVault(ctx, []byte("short"), "amber"); !errors.Is(err, api.ErrInvalidRequest) {
		t.Fatalf("short password = %v", err)
	}
	if err := a.CreateVault(ctx, []byte("correct horse battery"), "amber"); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(a.config.MountPoint, "notes.txt")
	if err := os.WriteFile(notes, []byte("written through the mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncUntilBackedUp(t, a)
	session, _ := a.currentSession()
	collector := orphans.New(orphans.Deps{Remote: session.client, References: session.referencedDriveIDs, Now: time.Now},
		orphans.Config{VaultID: session.vaultID, TreeFolderID: session.folders.Tree, TrashFolderID: session.folders.Trash})
	if result, err := collector.Collect(ctx); err != nil || result.Found != 0 {
		t.Fatalf("orphans in a synced vault: %+v, %v", result, err)
	}
	if err := a.Lock(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(notes); err == nil {
		t.Fatal("file still visible after lock")
	}
	if err := a.Unlock(ctx, []byte("wrong horse battery")); !errors.Is(err, api.ErrWrongPassword) {
		t.Fatalf("wrong password = %v", err)
	}
	if err := a.Unlock(ctx, []byte("correct horse battery")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(notes); err != nil || string(data) != "written through the mount" {
		t.Fatalf("after unlock read %q, %v", data, err)
	}

	b := newTestDaemon(t, fake, "birch")
	signIn(t, b)
	if err := b.JoinVault(ctx, []byte("correct horse battery"), "birch", api.JoinOnDemand); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(b.config.MountPoint, "notes.txt")); err != nil || string(data) != "written through the mount" {
		t.Fatalf("B read %q, %v", data, err)
	}
	assertFreeReclaims(t, b, "/notes.txt")
	if err := b.Download(ctx, "/notes.txt"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, b, "/notes.txt", api.FileLocal)
	assertFreeReclaims(t, b, "/notes.txt")

	page, err := b.Commits(ctx, "", 50, "/notes.txt")
	if err != nil || len(page.Commits) == 0 || page.Commits[0].DeviceName != "amber" || len(page.Devices) != 2 {
		t.Fatalf("B history = %+v, %v", page, err)
	}

	if err := b.RetireDevice(ctx, string(a.device)); err != nil {
		t.Fatal(err)
	}
	session, _ = a.currentSession()
	if _, err := session.devices.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	devices, _ := a.Devices(ctx)
	for _, device := range devices {
		if device.Retired != (device.ID == string(a.device)) || (device.ID == string(b.device) && device.Name != "birch") {
			t.Fatalf("A's device list = %+v", devices)
		}
	}
}

// syncUntilBackedUp retries because FUSE delivers RELEASE after close returns, and a file is
// queued for upload only once released.
func syncUntilBackedUp(t *testing.T, d *Daemon) {
	t.Helper()
	ctx := context.Background()
	var status api.Status
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := d.SyncNow(ctx); err != nil {
			t.Fatal(err)
		}
		if status, _ = d.Status(ctx); status.NotBackedUpFiles == 0 && !status.LastBackedUp.IsZero() {
			return
		}
	}
	t.Fatalf("status after sync = %+v", status)
}

// assertFreeReclaims frees a file that has a body on disk, whether partial from reads or
// complete from a download, and expects the body gone and the file still listed.
func assertFreeReclaims(t *testing.T, d *Daemon, vaultPath string) {
	t.Helper()
	session, _ := d.currentSession()
	entry, _ := session.workspace.Tree().Resolve(vaultPath)
	if !session.cache.HasBody(entry.ID) {
		t.Fatalf("%s has no body to free", vaultPath)
	}
	if err := d.Free(context.Background(), vaultPath); err != nil {
		t.Fatal(err)
	}
	if session.cache.HasBody(entry.ID) {
		t.Fatalf("%s still has a body after Free", vaultPath)
	}
	waitForState(t, d, vaultPath, api.FileCloudOnly)
}

func waitForState(t *testing.T, d *Daemon, vaultPath string, want api.FileState) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		entries, _ := d.Tree(context.Background(), path.Dir(vaultPath))
		for _, entry := range entries {
			if entry.Path == vaultPath && entry.State == want {
				return
			}
		}
	}
	t.Fatalf("%s never reached state %s", vaultPath, want)
}
