package devicetest

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/cosmoboy/vault/internal/fusefs"
	"github.com/cosmoboy/vault/internal/model"
)

// The helpers below drive the workspace exactly as the FUSE layer does.

func (d *Device) inode(p string) model.Ino {
	d.t.Helper()
	ino := d.Workspace.RootIno()
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part == "" {
			continue
		}
		attr, err := d.Workspace.Lookup(context.Background(), ino, part)
		if err != nil {
			d.t.Fatalf("%s: lookup %s: %v", d.ID, p, err)
		}
		ino = attr.Ino
	}
	return ino
}

func (d *Device) Exists(p string) bool {
	d.t.Helper()
	ino := d.Workspace.RootIno()
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		attr, err := d.Workspace.Lookup(context.Background(), ino, part)
		if err != nil {
			return false
		}
		ino = attr.Ino
	}
	return true
}

func (d *Device) Mkdir(p string) {
	d.t.Helper()
	if _, err := d.Workspace.Mkdir(context.Background(), d.inode(path.Dir(p)), path.Base(p)); err != nil {
		d.t.Fatalf("%s: mkdir %s: %v", d.ID, p, err)
	}
}

func (d *Device) WriteFile(p string, data []byte) {
	d.t.Helper()
	ctx := context.Background()
	var handle fusefs.Handle
	var err error
	if d.Exists(p) {
		handle, _, err = d.Workspace.Open(ctx, d.inode(p), true, true, fusefs.Caller{PID: 1})
	} else {
		_, handle, err = d.Workspace.Create(ctx, d.inode(path.Dir(p)), path.Base(p), false, fusefs.Caller{PID: 1})
	}
	if err != nil {
		d.t.Fatalf("%s: open %s: %v", d.ID, p, err)
	}
	if _, err := handle.WriteAt(ctx, data, 0); err != nil {
		d.t.Fatalf("%s: write %s: %v", d.ID, p, err)
	}
	if err := handle.Release(ctx); err != nil {
		d.t.Fatalf("%s: close %s: %v", d.ID, p, err)
	}
}

func (d *Device) ReadFile(p string) []byte {
	d.t.Helper()
	ctx := context.Background()
	handle, _, err := d.Workspace.Open(ctx, d.inode(p), false, false, fusefs.Caller{PID: 1})
	if err != nil {
		d.t.Fatalf("%s: open %s: %v", d.ID, p, err)
	}
	defer handle.Release(ctx)
	attr, _ := d.Workspace.Getattr(ctx, d.inode(p))
	buf := make([]byte, attr.Size)
	n, err := handle.ReadAt(ctx, buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		d.t.Fatalf("%s: read %s: %v", d.ID, p, err)
	}
	return buf[:n]
}

func (d *Device) Rename(from, to string) {
	d.t.Helper()
	err := d.Workspace.Rename(context.Background(), d.inode(path.Dir(from)), path.Base(from), d.inode(path.Dir(to)), path.Base(to), false)
	if err != nil {
		d.t.Fatalf("%s: rename %s -> %s: %v", d.ID, from, to, err)
	}
}

func (d *Device) Remove(p string) {
	d.t.Helper()
	ctx := context.Background()
	attr, err := d.Workspace.Getattr(ctx, d.inode(p))
	if err != nil {
		d.t.Fatal(err)
	}
	remove := d.Workspace.Unlink
	if attr.Folder {
		remove = d.Workspace.Rmdir
	}
	if err := remove(ctx, d.inode(path.Dir(p)), path.Base(p), fusefs.Caller{PID: 1}); err != nil {
		d.t.Fatalf("%s: remove %s: %v", d.ID, p, err)
	}
}

func (d *Device) Sync() {
	d.t.Helper()
	if err := d.Outgoing.FlushNow(context.Background()); err != nil {
		d.t.Fatalf("%s: sync: %v", d.ID, err)
	}
}

func (d *Device) ObjectID(p string) model.ObjectID {
	d.t.Helper()
	entry, ok := d.Workspace.Tree().Resolve(p)
	if !ok {
		d.t.Fatalf("%s: %s not in tree", d.ID, p)
	}
	return entry.ID
}
