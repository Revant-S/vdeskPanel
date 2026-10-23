package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"vault/internal/cache"
	"vault/internal/index"
	"vault/internal/model"
	"vault/internal/vaultfile"
)

const authenticateChunk = 1 << 20

// download makes the body complete and authentic, then marks it Local. It reports false
// without an error when the object changed meanwhile; the next round sees the new version.
func (r *Restorer) download(ctx context.Context, object index.Object) (bool, error) {
	err := r.fetchAuthenticated(ctx, object)
	if err != nil {
		if stale, staleErr := r.isStale(ctx, object); staleErr == nil && stale {
			return false, nil
		}
		return false, err
	}
	return r.markLocal(ctx, object)
}

func (r *Restorer) fetchAuthenticated(ctx context.Context, object index.Object) error {
	var bodyID vaultfile.BodyID
	copy(bodyID[:], object.BodyID)
	entry, err := r.deps.Cache.Open(ctx, object.ID, object.Size, object.State, bodyID)
	if err != nil {
		return fmt.Errorf("open body: %w", err)
	}
	defer entry.Close()
	if err := entry.EnsureCompleteParallel(ctx, r.config.RangesPerFile); err != nil {
		return fmt.Errorf("download body: %w", err)
	}
	return authenticate(ctx, entry)
}

// authenticate decrypts every segment, because a range fetch stores ciphertext unchecked. A
// segment that fails is refetched once by the cache before the read gives up.
func authenticate(ctx context.Context, entry *cache.Entry) error {
	buffer := make([]byte, authenticateChunk)
	size := entry.Size()
	for offset := int64(0); offset < size; offset += authenticateChunk {
		if _, err := entry.ReadAt(ctx, buffer, offset); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("authenticate body: %w", err)
		}
	}
	return nil
}

// markLocal only applies to the version that was downloaded and only while it still needs
// a body; a write or a remote change in between wins.
func (r *Restorer) markLocal(ctx context.Context, downloaded index.Object) (bool, error) {
	marked := false
	err := r.deps.Index.Update(ctx, func(tx *index.Tx) error {
		current, err := tx.Object(downloaded.ID)
		if errors.Is(err, index.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameVersionNeedingBody(current, downloaded) {
			return nil
		}
		marked = true
		return tx.SetLocalState(downloaded.ID, model.Local)
	})
	if err != nil {
		return false, fmt.Errorf("mark local: %w", err)
	}
	return marked, nil
}

func (r *Restorer) isStale(ctx context.Context, downloaded index.Object) (bool, error) {
	stale := false
	err := r.deps.Index.View(ctx, func(tx *index.Tx) error {
		current, err := tx.Object(downloaded.ID)
		if errors.Is(err, index.ErrNotFound) {
			stale = true
			return nil
		}
		stale = err == nil && !sameVersionNeedingBody(current, downloaded)
		return err
	})
	return stale, err
}

func sameVersionNeedingBody(current, downloaded index.Object) bool {
	return isLiveFile(current) && needsBody(current) && bytes.Equal(current.BodyID, downloaded.BodyID)
}
