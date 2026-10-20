package fusefs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// The vault stores only plain files and folders, so links, devices and extended attributes
// are refused. Reads of attributes report "none" rather than an error so cp -a and file
// managers carry on.

func (n *node) Symlink(context.Context, string, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EPERM
}

func (n *node) Link(context.Context, fs.InodeEmbedder, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EPERM
}

func (n *node) Mknod(context.Context, string, uint32, uint32, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EPERM
}

func (n *node) Setxattr(context.Context, string, []byte, uint32) syscall.Errno {
	return syscall.ENOTSUP
}

func (n *node) Removexattr(context.Context, string) syscall.Errno {
	return syscall.ENOTSUP
}

func (n *node) Getxattr(context.Context, string, []byte) (uint32, syscall.Errno) {
	return 0, syscall.ENODATA
}

func (n *node) Listxattr(context.Context, []byte) (uint32, syscall.Errno) {
	return 0, 0
}
