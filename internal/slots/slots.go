// Package slots bounds parallel transfers by size. A large file takes four slots and a small
// one takes one, so the configured file count still holds for large files, which bandwidth
// limits, while four times as many small files run at once, since each costs a round trip
// to Drive rather than bandwidth.
package slots

import (
	"context"

	"golang.org/x/sync/semaphore"
)

const (
	SmallFile    = 1 << 20
	perLargeFile = 4
)

type Limiter struct {
	slots    *semaphore.Weighted
	capacity int64
}

func New(files int) *Limiter {
	capacity := int64(max(files, 1) * perLargeFile)
	return &Limiter{slots: semaphore.NewWeighted(capacity), capacity: capacity}
}

// Acquire waits for room for a file of the given size and returns the function that frees it.
func (l *Limiter) Acquire(ctx context.Context, size int64) (func(), error) {
	weight := int64(perLargeFile)
	if size < SmallFile {
		weight = 1
	}
	if err := l.slots.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { l.slots.Release(weight) }, nil
}

// Wait returns once every acquired slot has been released.
func (l *Limiter) Wait() {
	l.slots.Acquire(context.Background(), l.capacity)
	l.slots.Release(l.capacity)
}
