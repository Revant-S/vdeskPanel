package fusefs

import (
	"context"
	"errors"
	"io"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type fileHandle struct {
	handle Handle
}

var (
	_ fs.FileReader   = (*fileHandle)(nil)
	_ fs.FileWriter   = (*fileHandle)(nil)
	_ fs.FileFsyncer  = (*fileHandle)(nil)
	_ fs.FileFlusher  = (*fileHandle)(nil)
	_ fs.FileReleaser = (*fileHandle)(nil)
)

func (h *fileHandle) Read(ctx context.Context, dest []byte, offset int64) (fuse.ReadResult, syscall.Errno) {
	n, err := h.handle.ReadAt(ctx, dest, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, toErrno(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *fileHandle) Write(ctx context.Context, data []byte, offset int64) (uint32, syscall.Errno) {
	n, err := h.handle.WriteAt(ctx, data, offset)
	if err != nil {
		return uint32(n), toErrno(err)
	}
	return uint32(n), 0
}

func (h *fileHandle) Fsync(ctx context.Context, _ uint32) syscall.Errno {
	return toErrno(h.handle.Sync(ctx))
}

func (h *fileHandle) Flush(context.Context) syscall.Errno {
	return 0
}

func (h *fileHandle) Release(ctx context.Context) syscall.Errno {
	return toErrno(h.handle.Release(ctx))
}
