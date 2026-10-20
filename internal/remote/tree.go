// Package remote adapts the Drive client to the small interfaces its consumers declare.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/syncer"
)

// Tree serves the outgoing pipeline.
type Tree struct {
	Client *drive.Client
}

var _ syncer.RemoteStore = Tree{}

func (t Tree) GenerateIDs(ctx context.Context, count int) ([]string, error) {
	ids, err := t.Client.GenerateIDs(ctx, count)
	return ids, translate(err)
}

// CreateFolder treats an existing folder with the same pre-generated ID as already created.
func (t Tree) CreateFolder(ctx context.Context, spec syncer.RemoteSpec) (model.RemoteFile, error) {
	file, err := t.Client.CreateFolder(ctx, newFile(spec))
	if errors.Is(err, drive.ErrAlreadyExists) {
		return t.Get(ctx, spec.ID)
	}
	return file, translate(err)
}

func (t Tree) CreateSmallFile(ctx context.Context, spec syncer.RemoteSpec, content io.ReaderAt, size int64) (model.RemoteFile, error) {
	file, err := t.Client.CreateSmallFile(ctx, newFile(spec), content, size)
	return file, translate(err)
}

func (t Tree) StartResumableUpload(ctx context.Context, spec syncer.RemoteSpec, size int64) (syncer.UploadSession, error) {
	session, err := t.Client.StartResumableUpload(ctx, newFile(spec), size)
	return syncer.UploadSession{URI: session.URI, Size: session.Size}, translate(err)
}

func (t Tree) ResumeUpload(ctx context.Context, session syncer.UploadSession, content io.ReaderAt, onConfirmed func(int64)) (model.RemoteFile, error) {
	file, err := t.Client.ResumeUpload(ctx, drive.UploadSession{URI: session.URI, Size: session.Size}, content, onConfirmed)
	return file, translate(err)
}

func (t Tree) Copy(ctx context.Context, sourceID string, spec syncer.RemoteSpec) (model.RemoteFile, error) {
	file, err := t.Client.Copy(ctx, sourceID, newFile(spec))
	return file, translate(err)
}

func (t Tree) Update(ctx context.Context, id string, change syncer.RemoteChange) (model.RemoteFile, error) {
	file, err := t.Client.Update(ctx, id, drive.MetadataChange{Name: change.Name, AddParents: change.AddParents,
		RemoveParents: change.RemoveParents, Properties: change.Properties, ModifiedTime: change.ModifiedTime})
	return file, translate(err)
}

func (t Tree) Delete(ctx context.Context, id string) error {
	return translate(t.Client.Delete(ctx, id))
}

func (t Tree) Get(ctx context.Context, id string) (model.RemoteFile, error) {
	file, err := t.Client.Get(ctx, id)
	return file, translate(err)
}

func newFile(spec syncer.RemoteSpec) drive.NewFile {
	return drive.NewFile{ID: spec.ID, Name: spec.Name, ParentID: spec.ParentID, ModifiedTime: spec.ModifiedTime, Properties: spec.Properties}
}

// translate keeps the Drive error in the chain and adds the consumer's sentinel beside it.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, drive.ErrNotFound):
		return fmt.Errorf("%w: %w", syncer.ErrRemoteNotFound, err)
	case errors.Is(err, drive.ErrAlreadyExists):
		return fmt.Errorf("%w: %w", syncer.ErrRemoteExists, err)
	case errors.Is(err, drive.ErrUploadSessionExpired):
		return fmt.Errorf("%w: %w", syncer.ErrUploadSessionExpired, err)
	}
	return err
}
