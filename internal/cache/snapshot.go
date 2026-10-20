package cache

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

// SnapshotFile is an upload-ready copy of a body under a fresh file key and BodyID.
type SnapshotFile struct {
	Path            string
	Header          vaultfile.Header
	PlaintextSize   int64
	PlaintextSHA256 [32]byte
	CiphertextMD5   [16]byte
	CiphertextSize  int64
}

func (c *Cache) snapshotDir(batchID string) string {
	return filepath.Join(c.dir, "snapshots", batchID)
}

// Snapshot reseals the complete live body into snapshots/<batchID>/<id> and fsyncs it, so the
// upload is isolated from later writes and survives a crash (arbitration R2, R13). Writers
// wait for the duration of the copy.
func (c *Cache) Snapshot(ctx context.Context, batchID string, id model.ObjectID) (SnapshotFile, error) {
	if err := validateID(batchID); err != nil {
		return SnapshotFile{}, err
	}
	entry, err := c.openExisting(ctx, id)
	if err != nil {
		return SnapshotFile{}, err
	}
	defer entry.Close()
	body, err := entry.object.completeBody()
	if err != nil {
		return SnapshotFile{}, err
	}
	dir := c.snapshotDir(batchID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SnapshotFile{}, fmt.Errorf("cache: create snapshot dir: %w", err)
	}
	path := filepath.Join(dir, string(id))
	result, err := c.resealInto(body, path)
	if err != nil {
		os.Remove(path)
		return SnapshotFile{}, fmt.Errorf("cache: snapshot %s: %w", id, err)
	}
	if err := syncDirs(dir, filepath.Dir(dir)); err != nil {
		return SnapshotFile{}, err
	}
	return SnapshotFile{
		Path: path, Header: result.Header, PlaintextSize: result.PlaintextSize,
		PlaintextSHA256: result.PlaintextSHA256, CiphertextMD5: result.CiphertextMD5,
		CiphertextSize: vaultfile.CiphertextSize(result.PlaintextSize),
	}, nil
}

func (c *Cache) resealInto(body *vaultfile.Body, path string) (vaultfile.ResealResult, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return vaultfile.ResealResult{}, err
	}
	defer file.Close()
	var result vaultfile.ResealResult
	err = c.withContent(func(content cipher.AEAD) error {
		result, err = vaultfile.Reseal(body, file, content, c.deps.Random)
		return err
	})
	if errors.As(err, new(*vaultfile.SegmentError)) {
		return result, fmt.Errorf("%w: %w", ErrCorruptBody, err)
	}
	if err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, fmt.Errorf("sync snapshot: %w", err)
	}
	return result, nil
}

func (o *object) completeBody() (*vaultfile.Body, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.completeLocked() {
		return nil, fmt.Errorf("cache: snapshot %s: %w", o.id, ErrIncompleteBody)
	}
	return o.body, nil
}

func (c *Cache) RemoveSnapshot(batchID string, id model.ObjectID) error {
	if err := validateID(batchID); err != nil {
		return err
	}
	if err := validateID(string(id)); err != nil {
		return err
	}
	dir := c.snapshotDir(batchID)
	if err := os.Remove(filepath.Join(dir, string(id))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cache: remove snapshot %s: %w", id, err)
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !isDirNotEmpty(err) {
		return fmt.Errorf("cache: remove snapshot dir: %w", err)
	}
	return nil
}

// Replace renames fromPath over the cached body, which then counts as complete. Entries opened
// before keep reading the old inode and are detached: their writes reach nothing (arbitration R9).
func (c *Cache) Replace(ctx context.Context, id model.ObjectID, fromPath string) error {
	if err := validateID(string(id)); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detachLocked(id)
	path := c.BodyPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cache: create shard dir for %s: %w", id, err)
	}
	if err := os.Rename(fromPath, path); err != nil {
		return fmt.Errorf("cache: replace body %s: %w", id, err)
	}
	if err := syncDirs(filepath.Dir(path), filepath.Dir(fromPath)); err != nil {
		return err
	}
	if err := c.deps.Bitmaps.PutSegmentBitmap(ctx, id, nil); err != nil {
		return fmt.Errorf("cache: clear bitmap %s: %w", id, err)
	}
	return nil
}

// Free deletes the local body. It refuses while a writer is open; the Dirty and Corrupt
// refusals of arbitration R30 belong to the caller, which knows the index state.
func (c *Cache) Free(ctx context.Context, id model.ObjectID) error {
	if err := validateID(string(id)); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if o, open := c.objects[id]; open && o.writers > 0 {
		return fmt.Errorf("cache: free %s: %w", id, ErrBusy)
	}
	c.detachLocked(id)
	if err := os.Remove(c.BodyPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cache: free %s: %w", id, err)
	}
	if err := c.deps.Bitmaps.PutSegmentBitmap(ctx, id, nil); err != nil {
		return fmt.Errorf("cache: clear bitmap %s: %w", id, err)
	}
	return nil
}

func isDirNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

func syncDirs(dirs ...string) error {
	for _, dir := range dirs {
		handle, err := os.Open(dir)
		if err != nil {
			return fmt.Errorf("cache: open dir for sync: %w", err)
		}
		err = handle.Sync()
		handle.Close()
		if err != nil {
			return fmt.Errorf("cache: sync dir: %w", err)
		}
	}
	return nil
}
