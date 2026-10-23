package workspace

import (
	"context"

	"vault/internal/model"
)

const invalidationQueueSize = 256

type entryInvalidation struct {
	parent  model.Ino
	name    string
	content bool
}

// queueEntryInvalidation defers a kernel invalidation until the current filesystem call has
// returned: the kernel holds directory locks during the call, so notifying from inside it
// would deadlock. A full queue drops the request; the short entry timeout covers it.
func (w *Workspace) queueEntryInvalidation(parent model.Ino, name string) {
	select {
	case w.pendingInvalidations <- entryInvalidation{parent: parent, name: name}:
	default:
	}
}

func (w *Workspace) queueContentInvalidation(ino model.Ino) {
	select {
	case w.pendingInvalidations <- entryInvalidation{parent: ino, content: true}:
	default:
	}
}

// RunInvalidations delivers queued invalidations until ctx ends.
func (w *Workspace) RunInvalidations(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case request := <-w.pendingInvalidations:
			invalidator := w.invalidator.Load()
			switch {
			case invalidator == nil:
			case request.content:
				(*invalidator).InvalidateContent(request.parent)
			default:
				(*invalidator).InvalidateEntry(request.parent, request.name)
			}
		}
	}
}
