package journal

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"log/slog"

	"vault/internal/crypto"
	"vault/internal/index"
	"vault/internal/model"
)

const (
	commitSuffix = ".vlog"
	packSuffix   = ".vpack"
	singlesKey   = "journal/singles"
)

type LogFile struct {
	DriveID    string
	FolderID   string
	Name       string
	Properties map[string]string
	Data       []byte
}

// LogRemote is the slice of Drive the journal needs. CreateFile reports created=false,
// without error, when a file with that Drive ID already exists.
type LogRemote interface {
	GenerateID(ctx context.Context) (string, error)
	CreateFile(ctx context.Context, file LogFile) (created bool, err error)
	Exists(ctx context.Context, driveID string) (bool, error)
	Download(ctx context.Context, driveID string) ([]byte, error)
	ListFolder(ctx context.Context, folderID string) ([]model.RemoteFile, error)
	Delete(ctx context.Context, driveID string) error
}

type Config struct {
	VaultID        string
	Device         model.DeviceID
	DeviceName     string
	OwnLogFolderID string
	PackEvery      int
}

type Deps struct {
	Remote  LogRemote
	Store   *index.Store
	Keyring *crypto.Keyring
	Codec   *Codec
	Clock   *Clock
	Logger  *slog.Logger
}

type Journal struct {
	Deps
	config Config
}

func New(deps Deps, config Config) *Journal {
	if config.PackEvery == 0 {
		config.PackEvery = 500
	}
	return &Journal{Deps: deps, config: config}
}

// Record publishes ops as one commit. It is idempotent: replaying with the same commit and
// Drive IDs after a crash reuses whatever already reached Drive instead of writing again.
func (j *Journal) Record(ctx context.Context, commitID, driveID string, ops []Op) (Commit, error) {
	if record, err := j.storedCommit(ctx, commitID); err == nil {
		return j.OpenRecord(record)
	} else if !errors.Is(err, index.ErrNotFound) {
		return Commit{}, err
	}
	exists, err := j.Remote.Exists(ctx, driveID)
	if err != nil {
		return Commit{}, fmt.Errorf("check commit %s: %w", commitID, err)
	}
	if exists {
		return j.adoptUploadedCommit(ctx, commitID, driveID)
	}
	commit, err := j.newCommit(ctx, commitID, ops)
	if err != nil {
		return Commit{}, err
	}
	sealed, err := j.seal(func(key cipher.AEAD) ([]byte, error) { return j.Codec.SealCommit(commit, key) })
	if err != nil {
		return Commit{}, err
	}
	file := LogFile{DriveID: driveID, FolderID: j.config.OwnLogFolderID, Name: commitID + commitSuffix,
		Properties: map[string]string{"vid": j.config.VaultID, "kind": "commit"}, Data: sealed}
	if _, err := j.Remote.CreateFile(ctx, file); err != nil {
		return Commit{}, fmt.Errorf("upload commit %s: %w", commitID, err)
	}
	return commit, j.storeOwnCommit(ctx, commit, sealed)
}

func (j *Journal) adoptUploadedCommit(ctx context.Context, commitID, driveID string) (Commit, error) {
	sealed, err := j.Remote.Download(ctx, driveID)
	if err != nil {
		return Commit{}, fmt.Errorf("download uploaded commit %s: %w", commitID, err)
	}
	commit, err := j.openCommit(commitID, sealed)
	if err != nil {
		return Commit{}, err
	}
	return commit, j.storeOwnCommit(ctx, commit, sealed)
}

func (j *Journal) newCommit(ctx context.Context, commitID string, ops []Op) (Commit, error) {
	commit := Commit{ID: commitID, Device: j.config.Device, DeviceName: j.config.DeviceName, Ops: ops}
	err := j.Store.View(ctx, func(tx *index.Tx) error {
		latest, err := tx.LatestCommitOf(j.config.Device)
		if err == nil {
			commit.Parents = append(commit.Parents, latest.ID)
		} else if !errors.Is(err, index.ErrNotFound) {
			return err
		}
		heads, err := tx.UnreferencedHeads(j.config.Device)
		for _, head := range heads {
			commit.Parents = append(commit.Parents, head.ID)
		}
		return err
	})
	commit.Clock = j.Clock.Next()
	return commit, err
}

func (j *Journal) storeOwnCommit(ctx context.Context, commit Commit, sealed []byte) error {
	return j.Store.Update(ctx, func(tx *index.Tx) error {
		record := index.CommitRecord{ID: commit.ID, Device: commit.Device, Clock: commit.Clock, Parents: commit.Parents,
			Sealed: sealed, Applied: true, Own: true, Referenced: true}
		if err := tx.PutCommit(record); err != nil {
			return err
		}
		for _, parentID := range commit.Parents {
			parent, err := tx.Commit(parentID)
			if errors.Is(err, index.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if err := tx.MarkDeviceReferenced(parent.Device, parent.Clock); err != nil {
				return err
			}
		}
		return incrementSingles(tx)
	})
}

func (j *Journal) OpenRecord(record index.CommitRecord) (Commit, error) {
	return j.openCommit(record.ID, record.Sealed)
}

func (j *Journal) openCommit(commitID string, sealed []byte) (Commit, error) {
	var commit Commit
	err := j.Keyring.Use(func(k *crypto.Keys) error {
		var err error
		commit, err = j.Codec.OpenCommit(commitID, sealed, k.Log)
		return err
	})
	return commit, err
}

func (j *Journal) seal(fn func(cipher.AEAD) ([]byte, error)) ([]byte, error) {
	var sealed []byte
	err := j.Keyring.Use(func(k *crypto.Keys) error {
		var err error
		sealed, err = fn(k.Log)
		return err
	})
	return sealed, err
}

func (j *Journal) storedCommit(ctx context.Context, commitID string) (index.CommitRecord, error) {
	var record index.CommitRecord
	err := j.Store.View(ctx, func(tx *index.Tx) error {
		var err error
		record, err = tx.Commit(commitID)
		return err
	})
	return record, err
}
