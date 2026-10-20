// Package journal is the encrypted, git-like activity log: commits, packs and the hybrid logical clock.
package journal

import (
	"log/slog"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

const (
	maxCounter = 9999
	maxSkew    = time.Hour
)

// Clock is a hybrid logical clock. Its stamps never go backwards even when the wall clock does.
type Clock struct {
	mu      sync.Mutex
	device  model.DeviceID
	wall    func() time.Time
	last    model.Stamp
	persist func(model.Stamp)
	logger  *slog.Logger
}

func NewClock(device model.DeviceID, wall func() time.Time, last model.Stamp, persist func(model.Stamp), logger *slog.Logger) *Clock {
	return &Clock{device: device, wall: wall, last: last, persist: persist, logger: logger}
}

func (c *Clock) Next() model.Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.wall().UnixMilli()
	next := model.Stamp{WallMillis: now, Device: c.device}
	if now <= c.last.WallMillis {
		next = model.Stamp{WallMillis: c.last.WallMillis, Counter: c.last.Counter + 1, Device: c.device}
	}
	if next.Counter > maxCounter {
		next = model.Stamp{WallMillis: next.WallMillis + 1, Device: c.device}
	}
	c.advanceTo(next)
	return next
}

// Observe moves the clock past a remote stamp, but never more than an hour beyond local time,
// so one device with a wrong clock cannot drag every other device into the future.
func (c *Clock) Observe(remote model.Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := c.wall().Add(maxSkew).UnixMilli()
	target := model.Stamp{WallMillis: remote.WallMillis, Counter: remote.Counter, Device: c.device}
	if remote.WallMillis > limit {
		c.logger.Warn("remote clock far ahead", "device", remote.Device, "aheadMillis", remote.WallMillis-c.wall().UnixMilli())
		target = model.Stamp{WallMillis: limit, Device: c.device}
	}
	if target.Compare(c.last) > 0 {
		c.advanceTo(target)
	}
}

func (c *Clock) Last() model.Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *Clock) advanceTo(stamp model.Stamp) {
	c.last = stamp
	if c.persist != nil {
		c.persist(stamp)
	}
}
