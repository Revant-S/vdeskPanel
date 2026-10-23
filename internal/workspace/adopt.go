package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"vault/internal/index"
	"vault/internal/model"
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
	if err := w.moveAdoptedBody(ctx, keep, absorbed); err != nil {
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

// moveAdoptedBody records the adoption before moving the body, so a crash before
// persistAdoption is finished at the next open.
func (w *Workspace) moveAdoptedBody(ctx context.Context, keep, absorbed model.ObjectID) error {
	if err := w.update(ctx, func(tx *index.Tx) error { return tx.SetKV(adoptionKeyPrefix+string(absorbed), []byte(keep)) }); err != nil {
		return err
	}
	return w.Cache.Replace(ctx, keep, w.Cache.BodyPath(absorbed))
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
		if err := tx.DeleteSegmentBitmap(keep); err != nil {
			return err
		}
		if err := tx.DeleteSegmentBitmap(replacement.ID); err != nil {
			return err
		}
		if err := tx.DeleteKV(adoptionKeyPrefix + string(replacement.ID)); err != nil {
			return err
		}
		return tx.DeleteObject(replacement.ID)
	})
}

const adoptionKeyPrefix = "workspace/adoption/"

// finishAdoptions completes adoptions a crash cut off after the body moved. Left undone, the
// replacement would be a live file with no body, which fails the next open.
func (w *Workspace) finishAdoptions(ctx context.Context) error {
	var adoptions map[string][]byte
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		var err error
		adoptions, err = tx.KVWithPrefix(adoptionKeyPrefix)
		return err
	})
	if err != nil {
		return err
	}
	for key, keep := range adoptions {
		if err := w.finishAdoption(ctx, model.ObjectID(keep), model.ObjectID(strings.TrimPrefix(key, adoptionKeyPrefix))); err != nil {
			return fmt.Errorf("finish adoption: %w", err)
		}
	}
	return nil
}

func (w *Workspace) finishAdoption(ctx context.Context, keep, absorbed model.ObjectID) error {
	replacement, err := w.indexObject(ctx, absorbed)
	if err != nil && !errors.Is(err, errNotIndexed) {
		return err
	}
	if err != nil || w.Cache.HasBody(absorbed) {
		// Done, or the body never moved and the next batch adopts again.
		return w.update(ctx, func(tx *index.Tx) error { return tx.DeleteKV(adoptionKeyPrefix + string(absorbed)) })
	}
	name, err := w.Names.DecryptName(absorbed, replacement.EncName)
	if err != nil {
		return err
	}
	encName, err := w.Names.EncryptName(keep, name)
	if err != nil {
		return err
	}
	return w.persistAdoption(ctx, keep, replacement, encName)
}
