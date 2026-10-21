// Package daemon runs one vault instance: the lock and unlock lifecycle, onboarding, and the
// session that ties the mount, the index, the cache and sync together.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/config"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/secret"
	"golang.org/x/oauth2"
)

// Notifier shows desktop notifications. It may be nil when no session bus is available.
type Notifier interface {
	Notify(ctx context.Context, summary, body string) error
}

type Deps struct {
	Layout    config.Layout
	Drive     DriveConnector
	Notifier  Notifier
	Hardening secret.HardeningReport
	Now       func() time.Time
	Logger    *slog.Logger
}

type Daemon struct {
	Deps
	mu           sync.Mutex
	config       config.Config
	state        api.VaultState
	device       model.DeviceID
	pendingToken *oauth2.Token
	session      *session
	message      string
}

var _ api.Service = (*Daemon)(nil)

func New(deps Deps, cfg config.Config) (*Daemon, error) {
	if err := deps.Layout.CreateDirs(); err != nil {
		return nil, err
	}
	device, err := loadOrCreateDeviceID(deps.Layout.DeviceIDFile)
	if err != nil {
		return nil, err
	}
	d := &Daemon{Deps: deps, config: cfg, device: device, state: api.StateNeedsSignIn}
	if _, err := os.Stat(deps.Layout.VaultHeaderFile); err == nil {
		d.state = api.StateLocked
	}
	return d, nil
}

func (d *Daemon) Status(ctx context.Context) (api.Status, error) {
	d.mu.Lock()
	status := api.Status{State: d.state, DeviceName: d.config.DeviceName, MountPoint: d.config.MountPoint,
		KeysMaySwap: d.Hardening.KeysMaySwap(), Message: d.message, VaultExists: d.state != api.StateNeedsSignIn && d.state != api.StateNeedsSetup}
	session := d.session
	d.mu.Unlock()
	if session != nil {
		session.fillStatus(ctx, &status)
	}
	return status, nil
}

// Subscribe sends a status event whenever the status changes, checking every two seconds.
func (d *Daemon) Subscribe(ctx context.Context) <-chan api.Event {
	events := make(chan api.Event, 4)
	go func() {
		defer close(events)
		var last api.Status
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if status, err := d.Status(ctx); err == nil && !sameStatus(status, last) {
				last = status
				select {
				case events <- api.Event{Kind: api.EventStatus, Status: &status}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return events
}

func sameStatus(a, b api.Status) bool {
	a.Restore, b.Restore = nil, nil
	return a == b
}

func (d *Daemon) setState(state api.VaultState, message string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state, d.message = state, message
}

func (d *Daemon) currentSession() (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return nil, api.ErrLocked
	}
	return d.session, nil
}

func loadOrCreateDeviceID(path string) (model.DeviceID, error) {
	if data, err := os.ReadFile(path); err == nil {
		return model.DeviceID(data), nil
	}
	device, err := model.NewDeviceID(randomSource{})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(device), 0o600); err != nil {
		return "", fmt.Errorf("save device id: %w", err)
	}
	return device, nil
}
