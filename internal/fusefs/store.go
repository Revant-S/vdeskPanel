// Package fusefs serves the vault as a FUSE mount. It holds only inode numbers; the Store
// behind it owns every mapping from inode to object.
package fusefs

import (
	"context"
	"errors"
	"io/fs"
	"syscall"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

type Attr struct {
	Ino        model.Ino
	Folder     bool
	Size       int64
	ModTime    time.Time
	Executable bool
}

type DirEntry struct {
	Name   string
	Ino    model.Ino
	Folder bool
}

type Caller struct {
	PID uint32
	UID uint32
}

type AttrChange struct {
	Size       *int64
	ModTime    *time.Time
	Executable *bool
}

type StatFS struct {
	TotalBytes     uint64
	FreeBytes      uint64
	AvailableBytes uint64
}

// Store answers every filesystem call. Errors may be io/fs sentinels or syscall.Errno values.
type Store interface {
	RootIno() model.Ino
	Lookup(ctx context.Context, parent model.Ino, name string) (Attr, error)
	Getattr(ctx context.Context, ino model.Ino) (Attr, error)
	ReadDir(ctx context.Context, ino model.Ino) ([]DirEntry, error)
	Mkdir(ctx context.Context, parent model.Ino, name string) (Attr, error)
	Create(ctx context.Context, parent model.Ino, name string, executable bool, caller Caller) (Attr, Handle, error)
	Open(ctx context.Context, ino model.Ino, writable, truncate bool, caller Caller) (handle Handle, keepCache bool, err error)
	Rename(ctx context.Context, parent model.Ino, name string, newParent model.Ino, newName string, noReplace bool) error
	Unlink(ctx context.Context, parent model.Ino, name string, caller Caller) error
	Rmdir(ctx context.Context, parent model.Ino, name string, caller Caller) error
	Setattr(ctx context.Context, ino model.Ino, change AttrChange) (Attr, error)
	StatFS(ctx context.Context) (StatFS, error)
}

type Handle interface {
	ReadAt(ctx context.Context, p []byte, offset int64) (int, error)
	WriteAt(ctx context.Context, p []byte, offset int64) (int, error)
	Sync(ctx context.Context) error
	Release(ctx context.Context) error
}

func toErrno(err error) syscall.Errno {
	var errno syscall.Errno
	switch {
	case err == nil:
		return 0
	case errors.As(err, &errno):
		return errno
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return syscall.EPERM
	case errors.Is(err, fs.ErrInvalid):
		return syscall.EINVAL
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	default:
		return syscall.EIO
	}
}
