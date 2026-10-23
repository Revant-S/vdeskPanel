package syncer

import (
	"context"
	"errors"
	"io"
	"time"

	"vault/internal/model"
)

var (
	ErrRemoteNotFound       = errors.New("syncer: remote object not found")
	ErrRemoteExists         = errors.New("syncer: remote object already exists")
	ErrUploadSessionExpired = errors.New("syncer: upload session expired")
)

type RemoteSpec struct {
	ID           string
	Name         string
	ParentID     string
	ModifiedTime time.Time
	Properties   map[string]string
}

type RemoteChange struct {
	Name          string
	AddParents    []string
	RemoveParents []string
	ModifiedTime  time.Time
	Properties    map[string]string
}

type UploadSession struct {
	URI  string `json:"uri"`
	Size int64  `json:"size"`
}

// RemoteStore is the part of Drive the outgoing pipeline uses. Implementations return errors
// matching ErrRemoteNotFound, ErrRemoteExists and ErrUploadSessionExpired.
type RemoteStore interface {
	GenerateIDs(ctx context.Context, count int) ([]string, error)
	CreateFolder(ctx context.Context, spec RemoteSpec) (model.RemoteFile, error)
	CreateSmallFile(ctx context.Context, spec RemoteSpec, content io.ReaderAt, size int64) (model.RemoteFile, error)
	StartResumableUpload(ctx context.Context, spec RemoteSpec, size int64) (UploadSession, error)
	ResumeUpload(ctx context.Context, session UploadSession, content io.ReaderAt, onConfirmed func(offset int64)) (model.RemoteFile, error)
	Copy(ctx context.Context, sourceID string, spec RemoteSpec) (model.RemoteFile, error)
	Update(ctx context.Context, id string, change RemoteChange) (model.RemoteFile, error)
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (model.RemoteFile, error)
}

const smallFileLimit = 5 << 20

// isServerError reports a 5xx answer from Drive, which for an upload session often means the
// session itself is broken and only a new one will work.
func isServerError(err error) bool {
	var status interface{ StatusCode() int }
	return errors.As(err, &status) && status.StatusCode() >= 500
}
