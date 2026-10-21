package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/config"
	"github.com/cosmoboy/vault/internal/devicetest"
	"github.com/cosmoboy/vault/internal/drive/drivefake"
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
	if err := a.SyncNow(ctx); err != nil {
		t.Fatal(err)
	}
	if status, _ := a.Status(ctx); status.NotBackedUpFiles != 0 || status.LastBackedUp.IsZero() {
		t.Fatalf("status after sync = %+v", status)
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
	page, err := b.Commits(ctx, "", 50, "/notes.txt")
	if err != nil || len(page.Commits) == 0 || page.Commits[0].DeviceName != "amber" || len(page.Devices) != 2 {
		t.Fatalf("B history = %+v, %v", page, err)
	}
}
