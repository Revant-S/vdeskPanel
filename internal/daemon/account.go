package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/oauth2"
	"vault/internal/api"
	"vault/internal/auth"
	"vault/internal/config"
	"vault/internal/crypto"
	"vault/internal/drive"
	"vault/internal/restore"
	"vault/internal/secret"
)

const (
	minimumPasswordLength = 12
	signInTimeout         = 10 * time.Minute
)

// BeginSignIn returns the Google consent URL. The client opens it; the daemon may have no
// display. The token stays in memory until a vault key exists to encrypt it with.
func (d *Daemon) BeginSignIn(context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), signInTimeout)
	authURL, wait, err := d.Drive.BeginSignIn(ctx)
	if err != nil {
		cancel()
		return "", err
	}
	go func() {
		defer cancel()
		token, err := wait(ctx)
		if err != nil {
			d.Logger.Warn("sign-in did not complete", "err", err)
			return
		}
		d.completeSignIn(ctx, token)
	}()
	return authURL, nil
}

func (d *Daemon) completeSignIn(ctx context.Context, token *oauth2.Token) {
	exists, err := d.vaultOnDrive(ctx, token)
	if err != nil {
		d.Logger.Warn("look for an existing vault", "err", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pendingToken = token
	if d.state == api.StateNeedsSignIn {
		d.state = api.StateNeedsSetup
		d.message = map[bool]string{true: "join", false: "create"}[exists]
	}
}

func (d *Daemon) vaultOnDrive(ctx context.Context, token *oauth2.Token) (bool, error) {
	client, err := d.Drive.Connect(ctx, d.Drive.TokenSource(ctx, token))
	if err != nil {
		return false, err
	}
	_, err = d.remoteHeader(ctx, client)
	if errors.Is(err, errNoRemoteHeader) {
		return false, nil
	}
	return err == nil, err
}

func (d *Daemon) SignOut(ctx context.Context) error {
	if _, err := d.currentSession(); err == nil {
		if err := d.Lock(ctx); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pendingToken = nil
	if err := os.Remove(d.Layout.TokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if d.state == api.StateNeedsSetup {
		d.state = api.StateNeedsSignIn
	}
	return nil
}

func (d *Daemon) CreateVault(ctx context.Context, password []byte, deviceName string) error {
	defer secret.Wipe(password)
	if len(password) < minimumPasswordLength {
		return fmt.Errorf("%w: the password needs at least %d characters", api.ErrInvalidRequest, minimumPasswordLength)
	}
	token, client, err := d.signedInClient(ctx)
	if err != nil {
		return err
	}
	if _, err := d.remoteHeader(ctx, client); !errors.Is(err, errNoRemoteHeader) {
		return fmt.Errorf("%w: this Google account already has a vault; join it instead", api.ErrInvalidRequest)
	}
	header, keyring, err := crypto.CreateVault(password, crypto.DefaultKDFParams(), rand.Reader)
	if err != nil {
		return err
	}
	if err := d.publishHeader(ctx, client, header); err != nil {
		keyring.Destroy()
		return err
	}
	return d.startVault(ctx, keyring, token, client, deviceName, restore.RestoreEverything)
}

func (d *Daemon) JoinVault(ctx context.Context, password []byte, deviceName string, mode api.JoinMode) error {
	token, client, err := d.signedInClient(ctx)
	if err != nil {
		secret.Wipe(password)
		return err
	}
	header, err := d.remoteHeader(ctx, client)
	if err != nil {
		secret.Wipe(password)
		return err
	}
	keyring, err := unlockHeader(header, password)
	if err != nil {
		return err
	}
	if err := d.saveLocalHeader(header); err != nil {
		keyring.Destroy()
		return err
	}
	restoreMode := restore.RestoreEverything
	if mode == api.JoinOnDemand {
		restoreMode = restore.OnDemand
	}
	return d.startVault(ctx, keyring, token, client, deviceName, restoreMode)
}

func (d *Daemon) signedInClient(ctx context.Context) (*oauth2.Token, *drive.Client, error) {
	d.mu.Lock()
	token := d.pendingToken
	d.mu.Unlock()
	if token == nil {
		return nil, nil, fmt.Errorf("%w: sign in to Google first", api.ErrInvalidRequest)
	}
	client, err := d.Drive.Connect(ctx, d.Drive.TokenSource(ctx, token))
	return token, client, err
}

// startVault finishes create or join: it saves the device name, mode and token, then
// mounts.
func (d *Daemon) startVault(ctx context.Context, keyring *crypto.Keyring, token *oauth2.Token, client *drive.Client, deviceName string, mode restore.Mode) error {
	if err := auth.NewTokenStore(d.Layout.TokenFile, keyring).Save(token); err != nil {
		keyring.Destroy()
		return err
	}
	if deviceName != "" {
		d.config.DeviceName = deviceName
		if err := config.Save(d.Layout.ConfigFile, d.config); err != nil {
			keyring.Destroy()
			return err
		}
	}
	if err := saveRestoreMode(d.Layout, mode); err != nil {
		keyring.Destroy()
		return err
	}
	return d.mountSession(ctx, keyring, client, mode)
}

func (d *Daemon) Unlock(ctx context.Context, password []byte) error {
	d.mu.Lock()
	if d.state != api.StateLocked {
		d.mu.Unlock()
		secret.Wipe(password)
		return fmt.Errorf("%w: the vault is %s", api.ErrInvalidRequest, d.state)
	}
	d.state = api.StateUnlocking
	d.mu.Unlock()
	if err := d.unlock(ctx, password); err != nil {
		d.setState(api.StateLocked, "")
		return err
	}
	return nil
}

func (d *Daemon) unlock(ctx context.Context, password []byte) error {
	data, err := os.ReadFile(d.Layout.VaultHeaderFile)
	if err != nil {
		secret.Wipe(password)
		return err
	}
	header, err := crypto.ParseVaultHeader(data)
	if err != nil {
		secret.Wipe(password)
		return headerError(err)
	}
	keyring, err := unlockHeader(header, password)
	if err != nil {
		return err
	}
	client, err := d.connectWithStoredToken(ctx, keyring)
	if err != nil {
		keyring.Destroy()
		return err
	}
	if err := d.checkRemoteHeader(ctx, client, header); err != nil {
		keyring.Destroy()
		return err
	}
	return d.mountSession(ctx, keyring, client, loadRestoreMode(d.Layout))
}

func (d *Daemon) connectWithStoredToken(ctx context.Context, keyring *crypto.Keyring) (*drive.Client, error) {
	store := auth.NewTokenStore(d.Layout.TokenFile, keyring)
	token, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load Google sign-in: %w", err)
	}
	return d.Drive.Connect(ctx, store.PersistingSource(d.Drive.TokenSource(context.Background(), token)))
}

// checkRemoteHeader refuses to mount a vault Drive no longer agrees with. Offline, the local
// copy is trusted and the check is skipped.
func (d *Daemon) checkRemoteHeader(ctx context.Context, client *drive.Client, local crypto.VaultHeader) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remote, err := d.remoteHeader(ctx, client)
	switch {
	case errors.Is(err, crypto.ErrUnsupportedFormat):
		return api.ErrNewerFormat
	case err != nil:
		d.Logger.Warn("could not check the vault header on Drive", "err", err)
		return nil
	case remote.VaultID != local.VaultID || !bytes.Equal(remote.WrappedMasterKey, local.WrappedMasterKey):
		return fmt.Errorf("%w: the vault on Drive is not the one this device belongs to", api.ErrInvalidRequest)
	}
	return nil
}

func (d *Daemon) mountSession(ctx context.Context, keyring *crypto.Keyring, client *drive.Client, mode restore.Mode) error {
	session, err := d.openSession(ctx, keyring, client, mode)
	if err != nil {
		keyring.Destroy()
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.session, d.state, d.message, d.pendingToken = session, api.StateMounted, "", nil
	return nil
}

func (d *Daemon) Lock(ctx context.Context) error {
	d.mu.Lock()
	session := d.session
	if session == nil {
		d.mu.Unlock()
		return nil
	}
	d.state = api.StateLocking
	d.mu.Unlock()
	session.close(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.session, d.state = nil, api.StateLocked
	return nil
}

func unlockHeader(header crypto.VaultHeader, password []byte) (*crypto.Keyring, error) {
	keyring, err := crypto.UnlockVault(header, password)
	if errors.Is(err, crypto.ErrWrongPassword) {
		return nil, api.ErrWrongPassword
	}
	return keyring, err
}

func headerError(err error) error {
	if errors.Is(err, crypto.ErrUnsupportedFormat) {
		return api.ErrNewerFormat
	}
	return err
}
