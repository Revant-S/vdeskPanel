// Package cache keeps VLT1 bodies on local disk, sparse while they are only partly downloaded.
package cache

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/bits-and-blooms/bitset"
	"vault/internal/model"
	"vault/internal/vaultfile"
)

var (
	ErrIncompleteBody = errors.New("cache: body is not fully downloaded")
	ErrBusy           = errors.New("cache: body is open for writing")
	ErrReadOnly       = errors.New("cache: entry is open read-only")
	ErrClosed         = errors.New("cache: entry is closed")
	ErrDetached       = errors.New("cache: body was freed or replaced while open")
	ErrNoBody         = fmt.Errorf("cache: no local body: %w", os.ErrNotExist)
	ErrCorruptBody    = errors.New("cache: local body failed authentication")
	ErrInvalidID      = errors.New("cache: invalid object or batch id")
	// ErrBodyMismatch also matches vaultfile.ErrTampered; the caller polls remote changes once
	// and retries before giving up (arbitration A4).
	ErrBodyMismatch = fmt.Errorf("cache: remote body id differs from the index: %w", vaultfile.ErrTampered)
)

// SegmentFetcher reads a ciphertext byte range of an object's current remote body. It writes
// the range's first byte at offset 0 of w.
type SegmentFetcher interface {
	FetchRange(ctx context.Context, id model.ObjectID, offset, length int64, w io.WriterAt) error
}

// BitmapStore persists which segments of a sparse body are present. SegmentBitmap returns nil
// when no bitmap is stored; PutSegmentBitmap with nil deletes it.
type BitmapStore interface {
	SegmentBitmap(ctx context.Context, id model.ObjectID) (*bitset.BitSet, error)
	PutSegmentBitmap(ctx context.Context, id model.ObjectID, bitmap *bitset.BitSet) error
}

// ContentCipher runs use with the vault's content subkey, holding the keyring for the call.
type ContentCipher func(use func(content cipher.AEAD) error) error

type Deps struct {
	Fetcher SegmentFetcher
	Bitmaps BitmapStore
	Content ContentCipher
	Random  io.Reader
	Logger  *slog.Logger
}

type Config struct {
	// RunSegments is how many segments one range fetch covers, aligned to multiples of itself.
	RunSegments int64
}

const DefaultRunSegments = 128

// Cache owns every open body. One shared object per oid backs all of its open entries, so
// concurrent handles see one size and one bitmap.
type Cache struct {
	dir     string
	deps    Deps
	config  Config
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	objects map[model.ObjectID]*object
}

func New(dir string, deps Deps, config Config) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if config.RunSegments <= 0 {
		config.RunSegments = DefaultRunSegments
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Cache{dir: dir, deps: deps, config: config, ctx: ctx, cancel: cancel, objects: map[model.ObjectID]*object{}}, nil
}

// Close stops background prefetches. Entries still open remain usable for present segments.
func (c *Cache) Close() {
	c.cancel()
	c.mu.Lock()
	open := make([]*object, 0, len(c.objects))
	for _, o := range c.objects {
		open = append(open, o)
	}
	c.mu.Unlock()
	for _, o := range open {
		o.stopPrefetch()
	}
}

func (c *Cache) BodyPath(id model.ObjectID) string {
	shard := string(id)
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(c.dir, shard, string(id))
}

// validateObjectID also requires two characters for the shard directory.
func validateObjectID(id model.ObjectID) error {
	if len(id) < 2 {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return validateID(string(id))
}

// validateID keeps object and batch IDs to an alphabet that is safe as a path component.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidID)
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return fmt.Errorf("%w: %q", ErrInvalidID, id)
		}
	}
	return nil
}

func (c *Cache) withContent(use func(content cipher.AEAD) error) error {
	return c.deps.Content(use)
}

// Create starts a new Local body for a file created on this device.
func (c *Cache) Create(ctx context.Context, id model.ObjectID) (*Entry, error) {
	if err := validateObjectID(id); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, open := c.objects[id]; open {
		return nil, fmt.Errorf("cache: create %s: %w", id, ErrBusy)
	}
	o, err := c.createObject(ctx, id)
	if err != nil {
		return nil, err
	}
	c.objects[id] = o
	return o.newEntry(true), nil
}

// Open returns a read-only entry. plaintextSize and expectedBodyID describe the remote body
// for CloudOnly and Partial states; a Local, Dirty or Corrupt body is trusted as complete and
// its size comes from the file itself.
func (c *Cache) Open(ctx context.Context, id model.ObjectID, plaintextSize int64, state model.LocalState, expectedBodyID vaultfile.BodyID) (*Entry, error) {
	return c.open(ctx, openRequest{id: id, size: plaintextSize, state: state, bodyID: expectedBodyID})
}

// OpenWritable is Open for a handle that may write; Free refuses while one is open.
func (c *Cache) OpenWritable(ctx context.Context, id model.ObjectID, plaintextSize int64, state model.LocalState, expectedBodyID vaultfile.BodyID) (*Entry, error) {
	return c.open(ctx, openRequest{id: id, size: plaintextSize, state: state, bodyID: expectedBodyID, writable: true})
}

type openRequest struct {
	id       model.ObjectID
	size     int64
	state    model.LocalState
	bodyID   vaultfile.BodyID
	writable bool
}

func (c *Cache) open(ctx context.Context, request openRequest) (*Entry, error) {
	if err := validateObjectID(request.id); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if o, open := c.objects[request.id]; open {
		return o.newEntry(request.writable), nil
	}
	var o *object
	var err error
	switch request.state {
	case model.CloudOnly, model.Partial:
		o, err = c.openSparseObject(ctx, request)
	default:
		o, err = c.openLocalObject(request.id)
	}
	if err != nil {
		return nil, err
	}
	c.objects[request.id] = o
	return o.newEntry(request.writable), nil
}

// detachLocked forgets the open object for id; its entries keep their file descriptor.
func (c *Cache) detachLocked(id model.ObjectID) {
	if o, open := c.objects[id]; open {
		delete(c.objects, id)
		o.markDetached()
	}
}
