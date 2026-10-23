package fusefs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
	"vault/internal/model"
)

type node struct {
	fs.Inode
	mount *Mount
	ino   model.Ino
}

var (
	_ fs.NodeLookuper    = (*node)(nil)
	_ fs.NodeGetattrer   = (*node)(nil)
	_ fs.NodeSetattrer   = (*node)(nil)
	_ fs.NodeReaddirer   = (*node)(nil)
	_ fs.NodeMkdirer     = (*node)(nil)
	_ fs.NodeCreater     = (*node)(nil)
	_ fs.NodeOpener      = (*node)(nil)
	_ fs.NodeRenamer     = (*node)(nil)
	_ fs.NodeUnlinker    = (*node)(nil)
	_ fs.NodeRmdirer     = (*node)(nil)
	_ fs.NodeStatfser    = (*node)(nil)
	_ fs.NodeOnForgetter = (*node)(nil)
)

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	attr, err := n.mount.store.Lookup(ctx, n.ino, name)
	if err != nil {
		return nil, toErrno(err)
	}
	return n.child(ctx, attr, out), 0
}

func (n *node) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	attr, err := n.mount.store.Getattr(ctx, n.ino)
	if err != nil {
		return toErrno(err)
	}
	n.mount.fillAttr(attr, &out.Attr)
	out.SetTimeout(n.mount.options.AttrTimeout)
	return 0
}

func (n *node) Setattr(ctx context.Context, _ fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	var change AttrChange
	if size, ok := in.GetSize(); ok {
		value := int64(size)
		change.Size = &value
	}
	if mtime, ok := in.GetMTime(); ok {
		change.ModTime = &mtime
	}
	if mode, ok := in.GetMode(); ok {
		executable := mode&0o111 != 0
		change.Executable = &executable
	}
	attr, err := n.mount.store.Setattr(ctx, n.ino, change)
	if err != nil {
		return toErrno(err)
	}
	n.mount.fillAttr(attr, &out.Attr)
	return 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.mount.store.ReadDir(ctx, n.ino)
	if err != nil {
		return nil, toErrno(err)
	}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, fuse.DirEntry{Name: entry.Name, Ino: uint64(entry.Ino), Mode: fileType(entry.Folder)})
	}
	return fs.NewListDirStream(list), 0
}

func (n *node) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	attr, err := n.mount.store.Mkdir(ctx, n.ino, name)
	if err != nil {
		return nil, toErrno(err)
	}
	return n.child(ctx, attr, out), 0
}

func (n *node) Create(ctx context.Context, name string, _ uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	attr, handle, err := n.mount.store.Create(ctx, n.ino, name, mode&0o111 != 0, callerOf(ctx))
	if err != nil {
		return nil, nil, 0, toErrno(err)
	}
	return n.child(ctx, attr, out), &fileHandle{handle: handle}, 0, 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	writable := flags&syscall.O_ACCMODE != syscall.O_RDONLY
	handle, keepCache, err := n.mount.store.Open(ctx, n.ino, writable, flags&syscall.O_TRUNC != 0, callerOf(ctx))
	if err != nil {
		return nil, 0, toErrno(err)
	}
	var fuseFlags uint32
	if keepCache {
		fuseFlags |= fuse.FOPEN_KEEP_CACHE
	}
	return &fileHandle{handle: handle}, fuseFlags, 0
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags&unix.RENAME_EXCHANGE != 0 {
		return syscall.EINVAL
	}
	target, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	return toErrno(n.mount.store.Rename(ctx, n.ino, name, target.ino, newName, flags&unix.RENAME_NOREPLACE != 0))
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return toErrno(n.mount.store.Unlink(ctx, n.ino, name, callerOf(ctx)))
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return toErrno(n.mount.store.Rmdir(ctx, n.ino, name, callerOf(ctx)))
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	stats, err := n.mount.store.StatFS(ctx)
	if err != nil {
		return toErrno(err)
	}
	const blockSize = 4096
	out.Bsize, out.Frsize, out.NameLen = blockSize, blockSize, 255
	out.Blocks, out.Bfree, out.Bavail = stats.TotalBytes/blockSize, stats.FreeBytes/blockSize, stats.AvailableBytes/blockSize
	return 0
}

func (n *node) OnForget() {
	n.mount.forgetInode(n.ino, &n.Inode)
}

func (n *node) child(ctx context.Context, attr Attr, out *fuse.EntryOut) *fs.Inode {
	n.mount.fillAttr(attr, &out.Attr)
	out.SetEntryTimeout(n.mount.options.EntryTimeout)
	out.SetAttrTimeout(n.mount.options.AttrTimeout)
	child := n.NewInode(ctx, &node{mount: n.mount, ino: attr.Ino}, fs.StableAttr{Mode: fileType(attr.Folder), Ino: uint64(attr.Ino)})
	n.mount.rememberInode(attr.Ino, child)
	return child
}

func callerOf(ctx context.Context) Caller {
	caller, ok := fuse.FromContext(ctx)
	if !ok {
		return Caller{}
	}
	return Caller{PID: caller.Pid, UID: caller.Uid}
}

func fileType(folder bool) uint32 {
	if folder {
		return fuse.S_IFDIR
	}
	return fuse.S_IFREG
}
