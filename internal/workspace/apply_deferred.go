package workspace

import (
	"context"
	"encoding/json"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/journal"
	"github.com/cosmoboy/vault/internal/model"
)

// resolveDeferredContent settles a remote edit parked while the file was open for writing.
// If the local writer changed the file, its edit is the newer one and the remote author
// trashes its own version on seeing it; otherwise the remote edit applies now.
func (w *Workspace) resolveDeferredContent(ctx context.Context, id model.ObjectID, wrote bool) error {
	if w.handles.hasWriter(id) {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	apply := &remoteApply{ws: w, ctx: ctx}
	err := w.update(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(id)
		if err != nil || object.DeferredContent == nil {
			return err
		}
		var deferred deferredContent
		if err := json.Unmarshal(object.DeferredContent, &deferred); err != nil {
			return err
		}
		object.DeferredContent = nil
		if err := tx.PutObject(object); err != nil || wrote {
			return err
		}
		apply.tx = tx
		apply.commit = journal.Commit{Device: deferred.Device, DeviceName: deferred.DeviceName}
		return apply.content(deferred.Op)
	})
	if err != nil {
		return err
	}
	for _, effect := range apply.after {
		effect()
	}
	return nil
}
