package cache

import (
	"context"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/vaultfile"
)

type segmentSpan struct {
	first, last int64
}

// fetchSegments makes segments first..last present, fetching whole aligned runs minus the
// segments already on disk. The header comes first when absent.
func (o *object) fetchSegments(ctx context.Context, first, last int64) error {
	o.fetchMu.Lock()
	defer o.fetchMu.Unlock()
	if err := o.fetchHeader(ctx); err != nil {
		return err
	}
	run := o.cache.config.RunSegments
	count := o.segmentCount()
	for runStart := first / run * run; runStart <= last; runStart += run {
		runEnd := min(runStart+run, count) - 1
		if err := o.fetchRun(ctx, runStart, runEnd); err != nil {
			return err
		}
	}
	return nil
}

func (o *object) segmentCount() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return vaultfile.SegmentCount(o.size)
}

func (o *object) fetchRun(ctx context.Context, runStart, runEnd int64) error {
	spans, err := o.missingSpans(runStart, runEnd)
	if err != nil || len(spans) == 0 {
		return err
	}
	for _, span := range spans {
		if err := o.fetchSpan(ctx, span); err != nil {
			return err
		}
	}
	return o.storeBitmap(ctx)
}

func (o *object) missingSpans(first, last int64) ([]segmentSpan, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.mirrorsRemote {
		return nil, nil
	}
	var spans []segmentSpan
	for index := first; index <= last; index++ {
		if o.present.Test(uint(index)) {
			continue
		}
		if o.detached {
			return nil, fmt.Errorf("cache: fetch %s: %w", o.id, ErrDetached)
		}
		if n := len(spans); n > 0 && spans[n-1].last == index-1 {
			spans[n-1].last = index
		} else {
			spans = append(spans, segmentSpan{index, index})
		}
	}
	return spans, nil
}

func (o *object) fetchSpan(ctx context.Context, span segmentSpan) error {
	o.mu.Lock()
	end := min(vaultfile.SegmentOffset(span.last+1), vaultfile.CiphertextSize(o.size))
	o.mu.Unlock()
	start := vaultfile.SegmentOffset(span.first)
	writer := io.NewOffsetWriter(o.file, start)
	if err := o.cache.deps.Fetcher.FetchRange(ctx, o.id, start, end-start, writer); err != nil {
		return fmt.Errorf("cache: fetch segments %d-%d of %s: %w", span.first, span.last, o.id, err)
	}
	o.mu.Lock()
	for index := span.first; index <= span.last; index++ {
		o.present.Set(uint(index))
	}
	o.mu.Unlock()
	return nil
}

// storeBitmap runs after the ranges were written but without an fsync: a bitmap that outlives
// its data after a crash reads as a bad segment, and a mirror refetches those (arbitration R3).
func (o *object) storeBitmap(ctx context.Context) error {
	o.mu.Lock()
	if o.detached || !o.mirrorsRemote {
		o.mu.Unlock()
		return nil
	}
	snapshot := o.present.Clone()
	o.mu.Unlock()
	if err := o.cache.deps.Bitmaps.PutSegmentBitmap(ctx, o.id, snapshot); err != nil {
		return fmt.Errorf("cache: store bitmap %s: %w", o.id, err)
	}
	return nil
}

// fetchHeader checks the remote header against the index before any segment is fetched, so
// segments of a different body never land in this file (arbitration R4).
func (o *object) fetchHeader(ctx context.Context) error {
	o.mu.Lock()
	present, detached := o.body != nil, o.detached
	o.mu.Unlock()
	if present {
		return nil
	}
	if detached {
		return fmt.Errorf("cache: fetch header %s: %w", o.id, ErrDetached)
	}
	buffer := &bufferWriter{data: make([]byte, vaultfile.HeaderSize)}
	if err := o.cache.deps.Fetcher.FetchRange(ctx, o.id, 0, vaultfile.HeaderSize, buffer); err != nil {
		return fmt.Errorf("cache: fetch header of %s: %w", o.id, err)
	}
	header, err := vaultfile.ParseHeader(buffer.data)
	if err != nil {
		return fmt.Errorf("cache: header of %s: %w", o.id, err)
	}
	if header.BodyID != o.expectedBodyID {
		return fmt.Errorf("cache: header of %s: %w", o.id, ErrBodyMismatch)
	}
	if _, err := o.file.WriteAt(buffer.data, 0); err != nil {
		return fmt.Errorf("cache: write header of %s: %w", o.id, err)
	}
	body, err := o.openBodyWithHeader()
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.body = body
	o.mu.Unlock()
	return nil
}

// dropSegment forgets a present segment that failed authentication so it is fetched again.
func (o *object) dropSegment(ctx context.Context, index int64) error {
	o.mu.Lock()
	o.present.Clear(uint(index))
	o.mu.Unlock()
	return o.storeBitmap(ctx)
}

type bufferWriter struct {
	data []byte
}

func (b *bufferWriter) WriteAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset+int64(len(p)) > int64(len(b.data)) {
		return 0, fmt.Errorf("cache: fetched bytes outside the requested range")
	}
	return copy(b.data[offset:], p), nil
}

// prefetchRun fetches the run starting at runStart in the background. At most one prefetch
// per object runs at a time; stopPrefetch cancels it and waits.
func (o *object) prefetchRun(runStart int64) {
	o.mu.Lock()
	if o.prefetching || o.stopped || o.detached || !o.mirrorsRemote || runStart >= vaultfile.SegmentCount(o.size) {
		o.mu.Unlock()
		return
	}
	o.prefetching = true
	o.prefetches.Add(1)
	o.mu.Unlock()
	go func() {
		defer o.prefetches.Done()
		runEnd := runStart + o.cache.config.RunSegments - 1
		if err := o.fetchSegments(o.ctx, runStart, min(runEnd, o.segmentCount()-1)); err != nil && o.ctx.Err() == nil {
			o.cache.deps.Logger.Debug("prefetch failed", "oid", string(o.id), "err", err)
		}
		o.mu.Lock()
		o.prefetching = false
		o.mu.Unlock()
	}()
}
