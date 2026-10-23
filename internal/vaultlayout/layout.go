// Package vaultlayout creates and finds the app's folders on Drive: Vault/ with tree/, log/,
// trash/ and devices/ under it.
package vaultlayout

import (
	"context"
	"fmt"

	"vault/internal/drive"
	"vault/internal/model"
)

const (
	VaultFolderName   = "Vault"
	HeaderFileName    = "vault.header"
	treeFolderName    = "tree"
	logFolderName     = "log"
	trashFolderName   = "trash"
	devicesFolderName = "devices"
)

type Folders struct {
	Vault   string
	Tree    string
	Log     string
	Trash   string
	Devices string
}

// EnsureFolders finds or creates the layout. It is safe to call on every start.
func EnsureFolders(ctx context.Context, client *drive.Client) (Folders, error) {
	vault, err := client.EnsureFolder(ctx, drive.NewFile{Name: VaultFolderName, ParentID: "root"})
	if err != nil {
		return Folders{}, fmt.Errorf("ensure Vault folder: %w", err)
	}
	folders := Folders{Vault: vault.ID}
	for name, target := range map[string]*string{treeFolderName: &folders.Tree, logFolderName: &folders.Log,
		trashFolderName: &folders.Trash, devicesFolderName: &folders.Devices} {
		folder, err := client.EnsureFolder(ctx, drive.NewFile{Name: name, ParentID: vault.ID})
		if err != nil {
			return Folders{}, fmt.Errorf("ensure %s folder: %w", name, err)
		}
		*target = folder.ID
	}
	return folders, nil
}

func EnsureDeviceLogFolder(ctx context.Context, client *drive.Client, folders Folders, device model.DeviceID) (string, error) {
	folder, err := client.EnsureFolder(ctx, drive.NewFile{Name: string(device), ParentID: folders.Log})
	if err != nil {
		return "", fmt.Errorf("ensure device log folder: %w", err)
	}
	return folder.ID, nil
}
