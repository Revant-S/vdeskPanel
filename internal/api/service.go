package api

import (
	"context"
	"errors"
)

var (
	ErrWrongPassword  = errors.New("api: wrong password")
	ErrLocked         = errors.New("api: vault is locked")
	ErrNotFound       = errors.New("api: not found")
	ErrBusy           = errors.New("api: busy")
	ErrNewerFormat    = errors.New("api: vault was written by a newer version")
	ErrInvalidRequest = errors.New("api: invalid request")
)

// Service is implemented by the daemon. Passwords arrive as byte slices the callee wipes.
type Service interface {
	Status(ctx context.Context) (Status, error)
	BeginSignIn(ctx context.Context) (authURL string, err error)
	SignOut(ctx context.Context) error
	CreateVault(ctx context.Context, password []byte, deviceName string) error
	JoinVault(ctx context.Context, password []byte, deviceName string, mode JoinMode) error
	Unlock(ctx context.Context, password []byte) error
	Lock(ctx context.Context) error

	Tree(ctx context.Context, path string) ([]TreeEntry, error)
	Commits(ctx context.Context, before string, limit int, path string) (CommitPage, error)
	Devices(ctx context.Context) ([]DeviceView, error)
	RetireDevice(ctx context.Context, deviceID string) error

	Trash(ctx context.Context) ([]TrashItem, error)
	RestoreTrash(ctx context.Context, id string) (restoredPath string, err error)
	PurgeTrash(ctx context.Context, id string) error
	EmptyTrash(ctx context.Context) (EmptyTrashResult, error)

	Download(ctx context.Context, path string) error
	Free(ctx context.Context, path string) error
	SyncNow(ctx context.Context) error
	Verify(ctx context.Context, deep bool) (VerifyReport, error)
	PauseRestore(ctx context.Context, paused bool) error

	Settings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, settings Settings) error

	// Subscribe delivers events until ctx ends. The channel is closed when it does.
	Subscribe(ctx context.Context) <-chan Event
}
