package remote

import (
	"bytes"
	"context"
	"errors"

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

func (l *Log) CreateFile(ctx context.Context, file journal.LogFile) (bool, error) {
	_, err := l.Client.CreateSmallFile(ctx, drive.NewFile{ID: file.DriveID, Name: file.Name, ParentID: file.FolderID,
		Properties: file.Properties}, bytes.NewReader(file.Data), int64(len(file.Data)))
	if errors.Is(err, drive.ErrAlreadyExists) {
		return false, nil
	}
	return err == nil, err
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
	_, err := d.Client.CreateSmallFile(ctx, drive.NewFile{Name: name, ParentID: folderID, Properties: properties},
		bytes.NewReader(data), int64(len(data)))
	return err
}
