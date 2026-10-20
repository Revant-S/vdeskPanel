package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

// AdoptReplacement completes a save that wrote a new file in place of a deleted original:
// the original object takes the new file's place and body, so the vault keeps one identity
// instead of a trash entry per save. Calling it again after it succeeded does nothing.
func (w *Workspace) AdoptReplacement(ctx context.Context, keep, absorbed model.ObjectID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	replacement, err := w.indexObject(ctx, absorbed)
	if errors.Is(err, errNotIndexed) {
		return nil
	}
	if err != nil {
		return err
	}
	placed, ok := w.tree.Get(absorbed)
	if !ok {
		return fmt.Errorf("replacement %s is not in the tree", absorbed)
	}
	encName, err := w.Names.EncryptName(keep, placed.Name)
	if err != nil {
		return err
	}
	if err := w.Cache.Replace(ctx, keep, w.Cache.BodyPath(absorbed)); err != nil {
		return fmt.Errorf("adopt body: %w", err)
	}
	if err := w.persistAdoption(ctx, keep, replacement, encName); err != nil {
		return err
	}
	if err := w.tree.Rebind(absorbed, keep); err != nil {
		return err
	}
	w.inodes.rebind(absorbed, keep)
	w.handles.rebind(absorbed, keep)
	return nil
}

func (w *Workspace) persistAdoption(ctx context.Context, keep model.ObjectID, replacement index.Object, encName string) error {
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		original, err := tx.Object(keep)
		if err != nil {
			return err
		}
		original.ParentID, original.EncName = replacement.ParentID, encName
		original.Size, original.ModTime, original.Executable = replacement.Size, replacement.ModTime, replacement.Executable
		original.State, original.BodyID, original.DeleteVer = model.Dirty, nil, model.Stamp{}
		original.ContentVer, original.PlacementVer = stamp, stamp
		if err := tx.PutObject(original); err != nil {
			return err
		}
		if err := tx.DeleteSegmentBitmap(replacement.ID); err != nil {
			return err
		}
		return tx.DeleteObject(replacement.ID)
	})
}
