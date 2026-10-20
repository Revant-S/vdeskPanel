package fusefs

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/model"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type Options struct {
	MountPoint   string
	UID, GID     uint32
	EntryTimeout time.Duration
	AttrTimeout  time.Duration
}

type Mount struct {
	store   Store
	options Options
	logger  *slog.Logger
	server  *fuse.Server
	mu      sync.Mutex
	inodes  map[model.Ino]*fs.Inode
}

func MountStore(store Store, options Options, logger *slog.Logger) (*Mount, error) {
	m := &Mount{store: store, options: options, logger: logger, inodes: map[model.Ino]*fs.Inode{}}
	root := &node{mount: m, ino: store.RootIno()}
	server, err := fs.Mount(options.MountPoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName:  "vault",
			Name:    "vault",
			Options: []string{"default_permissions"},
		},
		EntryTimeout:   &options.EntryTimeout,
		AttrTimeout:    &options.AttrTimeout,
		UID:            options.UID,
		GID:            options.GID,
		RootStableAttr: &fs.StableAttr{Ino: uint64(store.RootIno()), Mode: fuse.S_IFDIR},
	})
	if err != nil {
		return nil, fmt.Errorf("mount vault: %w", err)
	}
	m.rememberInode(store.RootIno(), &root.Inode)
	if err := server.WaitMount(); err != nil {
		server.Unmount()
		return nil, fmt.Errorf("wait for mount: %w", err)
	}
	m.server = server
	return m, nil
}

// Unmount fails with EBUSY while any program holds a file open.
func (m *Mount) Unmount() error {
	if err := m.server.Unmount(); err != nil {
		return fmt.Errorf("unmount vault: %w", err)
	}
	m.server.Wait()
	return nil
}

// UnmountLazy detaches the mount even with files open; their next access fails.
func (m *Mount) UnmountLazy() error {
	output, err := exec.Command("fusermount3", "-u", "-z", m.options.MountPoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lazy unmount: %w: %s", err, output)
	}
	m.server.Wait()
	return nil
}

// InvalidateContent drops the kernel's cached pages for a file. Call it outside store locks:
// the kernel may call back into the filesystem before it returns.
func (m *Mount) InvalidateContent(ino model.Ino) {
	if inode := m.inode(ino); inode != nil {
		inode.NotifyContent(0, 0)
	}
}

// InvalidateEntry makes the kernel look name up again in parent. Call it outside store locks.
func (m *Mount) InvalidateEntry(parent model.Ino, name string) {
	if inode := m.inode(parent); inode != nil {
		inode.NotifyEntry(name)
	}
}

func (m *Mount) fillAttr(attr Attr, out *fuse.Attr) {
	out.Ino = uint64(attr.Ino)
	out.Size = uint64(attr.Size)
	out.Blocks = (out.Size + 511) / 512
	out.Blksize = 4096
	out.SetTimes(nil, &attr.ModTime, &attr.ModTime)
	out.Owner = fuse.Owner{Uid: m.options.UID, Gid: m.options.GID}
	out.Nlink = 1
	switch {
	case attr.Folder:
		out.Mode, out.Nlink = fuse.S_IFDIR|0o755, 2
	case attr.Executable:
		out.Mode = fuse.S_IFREG | 0o755
	default:
		out.Mode = fuse.S_IFREG | 0o644
	}
}

func (m *Mount) rememberInode(ino model.Ino, inode *fs.Inode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inodes[ino] = inode
}

func (m *Mount) forgetInode(ino model.Ino, inode *fs.Inode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inodes[ino] == inode {
		delete(m.inodes, ino)
	}
}

func (m *Mount) inode(ino model.Ino) *fs.Inode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inodes[ino]
}
