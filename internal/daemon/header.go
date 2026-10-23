package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cosmoboy/vault/internal/config"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/restore"
	"github.com/cosmoboy/vault/internal/vaultlayout"
)

var errNoRemoteHeader = errors.New("daemon: no vault on Drive yet")

func (d *Daemon) remoteHeader(ctx context.Context, client *drive.Client) (crypto.VaultHeader, error) {
	folders, err := vaultlayout.EnsureFolders(ctx, client)
	if err != nil {
		return crypto.VaultHeader{}, err
	}
	files, err := client.ListChildren(ctx, folders.Vault)
	if err != nil {
		return crypto.VaultHeader{}, err
	}
	for _, file := range files {
		if file.Name != vaultlayout.HeaderFileName {
			continue
		}
		var data bytes.Buffer
		if err := client.DownloadAll(ctx, file.ID, &data); err != nil {
			return crypto.VaultHeader{}, err
		}
		return crypto.ParseVaultHeader(data.Bytes())
	}
	return crypto.VaultHeader{}, errNoRemoteHeader
}

func (d *Daemon) publishHeader(ctx context.Context, client *drive.Client, header crypto.VaultHeader) error {
	data, err := crypto.MarshalVaultHeader(header)
	if err != nil {
		return err
	}
	folders, err := vaultlayout.EnsureFolders(ctx, client)
	if err != nil {
		return err
	}
	_, err = client.CreateSmallFile(ctx, drive.NewFile{Name: vaultlayout.HeaderFileName, ParentID: folders.Vault},
		bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("upload vault header: %w", err)
	}
	return d.saveLocalHeader(header)
}

// The header holds no secret; a local copy lets unlock derive the token key before Drive is
// reachable.
func (d *Daemon) saveLocalHeader(header crypto.VaultHeader) error {
	data, err := crypto.MarshalVaultHeader(header)
	if err != nil {
		return err
	}
	return os.WriteFile(d.Layout.VaultHeaderFile, data, 0o600)
}

func restoreModeFile(layout config.Layout) string {
	return layout.VaultHeaderFile + ".mode"
}

func saveRestoreMode(layout config.Layout, mode restore.Mode) error {
	text := "everything"
	if mode == restore.OnDemand {
		text = "on-demand"
	}
	return os.WriteFile(restoreModeFile(layout), []byte(text), 0o600)
}

func loadRestoreMode(layout config.Layout) restore.Mode {
	if data, err := os.ReadFile(restoreModeFile(layout)); err == nil && string(data) == "on-demand" {
		return restore.OnDemand
	}
	return restore.RestoreEverything
}
