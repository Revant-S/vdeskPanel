// Package trash recognises files leaving the vault and keeps the trash within its retention.
package trash

import (
	"sync"
	"time"

	"vault/internal/model"
)

const maxTracked = 4096

type ProcessResolver interface {
	ProcessOf(threadID uint32) (uint32, error)
}

type readKey struct {
	id      model.ObjectID
	process uint32
}

type coverage struct {
	spans    [][2]int64
	size     int64
	whole    bool
	lastSeen time.Time
}

// MoveOutDetector labels a delete as a move out of the vault when the deleting process read
// the whole file shortly before: that is what mv to another filesystem does. FUSE only sees
// the delete, so this is the only signal there is.
type MoveOutDetector struct {
	mu     sync.Mutex
	procs  ProcessResolver
	window time.Duration
	now    func() time.Time
	reads  map[readKey]*coverage
}

func NewMoveOutDetector(procs ProcessResolver, window time.Duration, now func() time.Time) *MoveOutDetector {
	return &MoveOutDetector{procs: procs, window: window, now: now, reads: map[readKey]*coverage{}}
}

func (d *MoveOutDetector) ObserveRead(id model.ObjectID, threadID uint32, offset, length, size int64) {
	d.track(id, threadID, func(c *coverage) {
		c.size = size
		c.spans = mergeSpan(c.spans, [2]int64{offset, offset + length})
		c.whole = c.whole || (len(c.spans) == 1 && c.spans[0][0] <= 0 && c.spans[0][1] >= size)
	})
}

// ObserveCachedOpen counts an open whose content the kernel serves from its page cache:
// those reads never reach FUSE, so the open stands in for reading the whole file.
func (d *MoveOutDetector) ObserveCachedOpen(id model.ObjectID, threadID uint32) {
	d.track(id, threadID, func(c *coverage) { c.whole = true })
}

func (d *MoveOutDetector) ClassifyDelete(id model.ObjectID, threadID uint32) model.DeleteReason {
	process, err := d.procs.ProcessOf(threadID)
	if err != nil {
		return model.ReasonDeleted
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.reads[readKey{id: id, process: process}]
	if ok && c.whole && d.now().Sub(c.lastSeen) <= d.window {
		return model.ReasonMovedOut
	}
	return model.ReasonDeleted
}

func (d *MoveOutDetector) track(id model.ObjectID, threadID uint32, update func(*coverage)) {
	process, err := d.procs.ProcessOf(threadID)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	key := readKey{id: id, process: process}
	c := d.reads[key]
	if c == nil {
		d.evictStale()
		c = &coverage{}
		d.reads[key] = c
	}
	update(c)
	c.lastSeen = d.now()
}

func (d *MoveOutDetector) evictStale() {
	if len(d.reads) < maxTracked {
		return
	}
	cutoff := d.now().Add(-d.window)
	for key, c := range d.reads {
		if c.lastSeen.Before(cutoff) {
			delete(d.reads, key)
		}
	}
	for key := range d.reads {
		if len(d.reads) < maxTracked {
			return
		}
		delete(d.reads, key)
	}
}

func mergeSpan(spans [][2]int64, span [2]int64) [][2]int64 {
	var out [][2]int64
	for _, existing := range spans {
		if existing[1] < span[0] || span[1] < existing[0] {
			out = append(out, existing)
			continue
		}
		span = [2]int64{min(existing[0], span[0]), max(existing[1], span[1])}
	}
	return append(out, span)
}
