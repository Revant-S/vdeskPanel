// Package workspace is the local, authoritative view of the vault. It implements the
// filesystem store over the tree, index and cache, and keeps the three consistent.
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"vault/internal/cache"
	"vault/internal/index"
	"vault/internal/model"
	"vault/internal/tree"
)

type NameCipher interface {
	EncryptName(id model.ObjectID, name string) (string, error)
	DecryptName(id model.ObjectID, encoded string) (string, error)
}

type StampSource interface {
	Next() model.Stamp
}

// Invalidator tells the kernel to forget cached entries and pages after a change it did not
// make itself. It is attached once the mount exists.
type Invalidator interface {
	InvalidateEntry(parent model.Ino, name string)
	InvalidateContent(ino model.Ino)
}

// DeleteClassifier labels a delete as a move out of the vault when the deleting process
// recently read the whole file.
type DeleteClassifier interface {
	ClassifyDelete(id model.ObjectID, processID uint32) model.DeleteReason
}

// ReadObserver learns which processes read which byte ranges, for move-out detection.
type ReadObserver interface {
	ObserveRead(id model.ObjectID, processID uint32, offset, length, size int64)
	ObserveCachedOpen(id model.ObjectID, processID uint32)
}

type Deps struct {
	Index  *index.Store
	Cache  *cache.Cache
	Names  NameCipher
	Clock  StampSource
	Random io.Reader
	Now    func() time.Time
	Logger *slog.Logger
	// Optional hooks filled in by later build steps.
	DenyRead func(processID uint32) bool
	Classify DeleteClassifier
	Reads    ReadObserver
	DiskFree func() (total, free, available uint64, err error)
	// WantLocal asks for a background download, after a remote change replaced a file that
	// was fully on this device.
	WantLocal func(id model.ObjectID)
}

type Config struct {
	Device      model.DeviceID
	DeviceName  string
	UID         uint32
	QuietPeriod time.Duration
	DeleteHold  time.Duration
}

func (c *Config) desktopTrashName() string {
	return fmt.Sprintf(".Trash-%d", c.UID)
}

type Workspace struct {
	Deps
	config               Config
	tree                 *tree.Tree
	mu                   sync.Mutex
	inodes               *inodeMap
	handles              *handleRegistry
	frozen               atomic.Bool
	invalidator          atomic.Pointer[Invalidator]
	pendingInvalidations chan entryInvalidation
}

// Open loads the index into an in-memory tree, decrypting names in parallel.
func Open(ctx context.Context, deps Deps, config Config) (*Workspace, error) {
	repairer := &Workspace{Deps: deps, config: config}
	if _, err := repairer.repairOrphans(ctx); err != nil {
		return nil, fmt.Errorf("repair orphaned objects: %w", err)
	}
	if err := repairer.finishAdoptions(ctx); err != nil {
		return nil, err
	}
	if err := repairer.finishBodyMoves(ctx); err != nil {
		return nil, err
	}
	built, err := tree.Build(ctx, liveRows(ctx, deps.Index), deps.Names.DecryptName, runtime.GOMAXPROCS(0))
	if err != nil {
		return nil, fmt.Errorf("build tree: %w", err)
	}
	ws := &Workspace{Deps: deps, config: config, tree: built, inodes: newInodeMap(), handles: newHandleRegistry(),
		pendingInvalidations: make(chan entryInvalidation, invalidationQueueSize)}
	if err := ws.finishRenamesOver(ctx); err != nil {
		return nil, err
	}
	if err := ws.finishShrinks(ctx); err != nil {
		return nil, err
	}
	if err := ws.recoverInterruptedWrites(ctx); err != nil {
		return nil, err
	}
	return ws, nil
}

func (w *Workspace) AttachInvalidator(invalidator Invalidator) {
	w.invalidator.Store(&invalidator)
}

// Freeze makes every later filesystem call fail with EIO and detaches open handles, so a
// lock can proceed while programs still hold files open.
func (w *Workspace) Freeze() {
	w.frozen.Store(true)
	w.handles.detachAll()
}

func (w *Workspace) Tree() *tree.Tree {
	return w.tree
}

func (w *Workspace) checkActive() error {
	if w.frozen.Load() {
		return syscall.EIO
	}
	return nil
}

func liveRows(ctx context.Context, store *index.Store) func(yield func(tree.EncryptedEntry, error) bool) {
	return func(yield func(tree.EncryptedEntry, error) bool) {
		err := store.View(ctx, func(tx *index.Tx) error {
			for object, err := range tx.AllObjects() {
				if err != nil {
					return err
				}
				if !isLive(object) {
					continue
				}
				if !yield(encryptedEntryOf(object), nil) {
					return nil
				}
			}
			return nil
		})
		if err != nil {
			yield(tree.EncryptedEntry{}, err)
		}
	}
}

// isLive implements the delete tombstone rule: an object stays visible while its content is
// newer than its delete.
func isLive(object index.Object) bool {
	return object.TrashID == "" && (object.DeleteVer.IsZero() || object.ContentVer.After(object.DeleteVer))
}

func encryptedEntryOf(o index.Object) tree.EncryptedEntry {
	return tree.EncryptedEntry{ID: o.ID, ParentID: o.ParentID, EncName: o.EncName, Kind: o.Kind, Size: o.Size,
		ModTime: o.ModTime, State: o.State, Executable: o.Executable, LocalOnly: o.LocalOnly}
}

// appendOp records op in the same transaction as the object change it describes. The stamp
// is the version the change gives the object.
func appendOp(tx *index.Tx, op model.LocalOp, stamp model.Stamp, notBefore time.Time) error {
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = tx.AppendOp(index.PendingOp{ObjectID: op.ObjectID, Kind: string(op.Kind), Payload: payload,
		Stamp: stamp, NotBefore: notBefore})
	return err
}

func (w *Workspace) quietDeadline() time.Time {
	return w.Now().Add(w.config.QuietPeriod)
}

func (w *Workspace) update(ctx context.Context, fn func(*index.Tx) error) error {
	return w.Index.Update(ctx, fn)
}
