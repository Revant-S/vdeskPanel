package workspace

import (
	"context"
	"errors"
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
	"vault/internal/crypto"
	"vault/internal/fusefs"
	"vault/internal/model"
)

var _ fusefs.Store = (*Workspace)(nil)

func (w *Workspace) RootIno() model.Ino {
	return rootIno
}

func (w *Workspace) Lookup(_ context.Context, parent model.Ino, name string) (fusefs.Attr, error) {
	parentID, err := w.objectOf(parent)
	if err != nil {
		return fusefs.Attr{}, err
	}
	entry, ok := w.tree.Lookup(parentID, name)
	if !ok {
		return fusefs.Attr{}, fs.ErrNotExist
	}
	return w.attrOf(entry), nil
}

func (w *Workspace) Getattr(_ context.Context, ino model.Ino) (fusefs.Attr, error) {
	entry, err := w.entryOf(ino)
	if err != nil {
		return fusefs.Attr{}, err
	}
	return w.attrOf(entry), nil
}

func (w *Workspace) ReadDir(_ context.Context, ino model.Ino) ([]fusefs.DirEntry, error) {
	entry, err := w.entryOf(ino)
	if err != nil {
		return nil, err
	}
	if !entry.IsFolder() {
		return nil, syscall.ENOTDIR
	}
	children := w.tree.Children(entry.ID)
	out := make([]fusefs.DirEntry, 0, len(children))
	for _, child := range children {
		out = append(out, fusefs.DirEntry{Name: child.Name, Ino: w.inodes.inodeOf(child.ID), Folder: child.IsFolder()})
	}
	return out, nil
}

func (w *Workspace) StatFS(context.Context) (fusefs.StatFS, error) {
	if w.DiskFree == nil {
		return fusefs.StatFS{}, nil
	}
	total, free, available, err := w.DiskFree()
	return fusefs.StatFS{TotalBytes: total, FreeBytes: free, AvailableBytes: available}, err
}

func StatFSOf(dir string) func() (total, free, available uint64, err error) {
	return func() (uint64, uint64, uint64, error) {
		var stats unix.Statfs_t
		if err := unix.Statfs(dir, &stats); err != nil {
			return 0, 0, 0, err
		}
		size := uint64(stats.Bsize)
		return stats.Blocks * size, stats.Bfree * size, stats.Bavail * size, nil
	}
}

func (w *Workspace) objectOf(ino model.Ino) (model.ObjectID, error) {
	if err := w.checkActive(); err != nil {
		return "", err
	}
	id, ok := w.inodes.objectOf(ino)
	if !ok {
		return "", fs.ErrNotExist
	}
	return id, nil
}

func (w *Workspace) entryOf(ino model.Ino) (model.Entry, error) {
	id, err := w.objectOf(ino)
	if err != nil {
		return model.Entry{}, err
	}
	entry, ok := w.tree.Get(id)
	if !ok {
		return model.Entry{}, fs.ErrNotExist
	}
	return entry, nil
}

func (w *Workspace) attrOf(entry model.Entry) fusefs.Attr {
	return fusefs.Attr{Ino: w.inodes.inodeOf(entry.ID), Folder: entry.IsFolder(), Size: entry.Size,
		ModTime: entry.ModTime, Executable: entry.Executable}
}

func nameError(err error) error {
	switch {
	case errors.Is(err, crypto.ErrNameTooLong):
		return syscall.ENAMETOOLONG
	case errors.Is(err, crypto.ErrInvalidName):
		return syscall.EINVAL
	}
	return err
}
