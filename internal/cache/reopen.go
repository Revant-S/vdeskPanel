package cache

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bits-and-blooms/bitset"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

// openExisting opens a body the caller has no index state for: without a bitmap record it is
// a complete local-authored body, otherwise a mirror.
func (c *Cache) openExisting(ctx context.Context, id model.ObjectID) (*Entry, error) {
	if err := validateObjectID(id); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if o, open := c.objects[id]; open {
		return o.newEntry(false), nil
	}
	stored, err := c.deps.Bitmaps.SegmentBitmap(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("cache: load bitmap %s: %w", id, err)
	}
	var o *object
	if stored == nil {
		o, err = c.openLocalObject(id)
	} else {
		o, err = c.openStoredMirror(id, stored)
	}
	if err != nil {
		return nil, err
	}
	c.objects[id] = o
	return o.newEntry(false), nil
}

// openStoredMirror trusts the BodyID in the file's own header, which was checked against the
// index when it was fetched.
func (c *Cache) openStoredMirror(id model.ObjectID, present *bitset.BitSet) (*object, error) {
	file, err := os.OpenFile(c.BodyPath(id), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cache: open %s: %w", id, ErrNoBody)
	}
	if err != nil {
		return nil, fmt.Errorf("cache: open body %s: %w", id, err)
	}
	o := c.newObject(id, file)
	o.mirrorsRemote, o.present = true, present
	if err := o.openMirrorFromFile(); err != nil {
		file.Close()
		return nil, err
	}
	return o, nil
}

func (o *object) openMirrorFromFile() error {
	info, err := o.file.Stat()
	if err != nil {
		return fmt.Errorf("cache: stat body %s: %w", o.id, err)
	}
	if o.size, err = vaultfile.PlaintextSize(info.Size()); err != nil {
		return fmt.Errorf("cache: body %s: %w", o.id, err)
	}
	header, err := vaultfile.ReadHeader(o.file)
	if err != nil {
		return nil
	}
	o.expectedBodyID = header.BodyID
	o.body, err = o.openBodyWithHeader()
	return err
}
