package workspace

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"vault/internal/index"
	"vault/internal/model"
)

const shrinkKeyPrefix = "workspace/shrink/"

// A shrink inside an earlier segment rewrites that segment in place before cutting the file,
// so a crash in between leaves a body whose old length no longer matches its last segment.
// Recording the target first lets the next open finish it.
func (w *Workspace) recordShrink(ctx context.Context, id model.ObjectID, size int64) error {
	return w.Index.Update(ctx, func(tx *index.Tx) error {
		return tx.SetKV(shrinkKeyPrefix+string(id), []byte(strconv.FormatInt(size, 10)))
	})
}

func (w *Workspace) clearShrink(ctx context.Context, id model.ObjectID) error {
	return w.Index.Update(ctx, func(tx *index.Tx) error { return tx.DeleteKV(shrinkKeyPrefix + string(id)) })
}

// finishShrinks completes shrinks a crash interrupted. A failed repair is logged and left:
// it must never be what keeps the vault from opening.
func (w *Workspace) finishShrinks(ctx context.Context) error {
	var shrinks map[string][]byte
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		var err error
		shrinks, err = tx.KVWithPrefix(shrinkKeyPrefix)
		return err
	})
	if err != nil {
		return err
	}
	for key, value := range shrinks {
		id := model.ObjectID(strings.TrimPrefix(key, shrinkKeyPrefix))
		target, err := strconv.ParseInt(string(value), 10, 64)
		if err == nil && w.Cache.HasBody(id) {
			if finished, err := w.Cache.FinishInterruptedShrink(id, target); err != nil {
				w.Logger.Warn("finish a shrink a crash cut short", "oid", id, "err", err)
			} else if finished {
				w.Logger.Warn("finished a shrink a crash cut short", "oid", id)
			}
		}
		if err := w.clearShrink(ctx, id); err != nil {
			return fmt.Errorf("clear shrink record: %w", err)
		}
	}
	return nil
}
