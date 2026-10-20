package cache

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// EnsureCompleteParallel is EnsureComplete with up to parallel range requests in flight, each
// covering at most one aligned run. Every finished range is recorded in the bitmap even if
// ctx ends meanwhile, so a restart never downloads it again.
func (e *Entry) EnsureCompleteParallel(ctx context.Context, parallel int) error {
	if err := e.checkOpen(); err != nil {
		return err
	}
	if e.object.complete() {
		return nil
	}
	return e.object.fetchAllParallel(ctx, max(parallel, 1))
}

func (o *object) fetchAllParallel(ctx context.Context, parallel int) error {
	o.fetchMu.Lock()
	defer o.fetchMu.Unlock()
	if err := o.fetchHeader(ctx); err != nil {
		return err
	}
	spans, err := o.missingSpansByRun()
	if err != nil {
		return err
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallel)
	for _, span := range spans {
		if groupCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := o.fetchSpan(groupCtx, span); err != nil {
				return err
			}
			return o.storeBitmap(context.WithoutCancel(groupCtx))
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

func (o *object) missingSpansByRun() ([]segmentSpan, error) {
	run, count := o.cache.config.RunSegments, o.segmentCount()
	var spans []segmentSpan
	for runStart := int64(0); runStart < count; runStart += run {
		missing, err := o.missingSpans(runStart, min(runStart+run, count)-1)
		if err != nil {
			return nil, err
		}
		spans = append(spans, missing...)
	}
	return spans, nil
}
