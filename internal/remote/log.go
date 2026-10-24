package remote

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"

	"vault/internal/devices"
	"vault/internal/drive"
	"vault/internal/journal"
	"vault/internal/model"
)

// Log serves the journal.
type Log struct {
	Client *drive.Client
}

var (
	_ journal.LogRemote = (*Log)(nil)
	_ devices.Remote    = (*Devices)(nil)
)

func (l *Log) GenerateID(ctx context.Context) (string, error) {
	ids, err := l.Client.GenerateIDs(ctx, 1)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// CreateFile uploads a commit or pack and checks Drive stored exactly those bytes: packing
// deletes the single commits afterwards, so a damaged pack would lose history for good.
func (l *Log) CreateFile(ctx context.Context, file journal.LogFile) (bool, error) {
	created, err := l.Client.CreateSmallFile(ctx, drive.NewFile{ID: file.DriveID, Name: file.Name, ParentID: file.FolderID,
		Properties: file.Properties}, bytes.NewReader(file.Data), int64(len(file.Data)))
	if errors.Is(err, drive.ErrAlreadyExists) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, checkStored(ctx, l.Client, created, file.Data)
}

func checkStored(ctx context.Context, client *drive.Client, created model.RemoteFile, sent []byte) error {
	sum := md5.Sum(sent)
	if created.MD5 == hex.EncodeToString(sum[:]) {
		return nil
	}
	if err := client.Delete(ctx, created.ID); err != nil {
		return fmt.Errorf("drive stored %s wrongly, and removing it failed: %w", created.ID, err)
	}
	return fmt.Errorf("drive stored %s with checksum %q, not the bytes sent; removed it", created.ID, created.MD5)
}

func (l *Log) Exists(ctx context.Context, driveID string) (bool, error) {
	_, err := l.Client.Get(ctx, driveID)
	if errors.Is(err, drive.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (l *Log) Download(ctx context.Context, driveID string) ([]byte, error) {
	var buffer bytes.Buffer
	err := l.Client.DownloadAll(ctx, driveID, &buffer)
	return buffer.Bytes(), err
}

func (l *Log) ListFolder(ctx context.Context, folderID string) ([]model.RemoteFile, error) {
	return l.Client.ListChildren(ctx, folderID)
}

func (l *Log) Delete(ctx context.Context, driveID string) error {
	return l.Client.Delete(ctx, driveID)
}

func IsNotFound(err error) bool {
	return errors.Is(err, drive.ErrNotFound)
}

// Devices serves the device registry.
type Devices struct {
	Log
}

func (d *Devices) Upload(ctx context.Context, folderID, name string, properties map[string]string, data []byte) error {
	created, err := d.Client.CreateSmallFile(ctx, drive.NewFile{Name: name, ParentID: folderID, Properties: properties},
		bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	return checkStored(ctx, d.Client, created, data)
}
