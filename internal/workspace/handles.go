package workspace

import (
	"sync"

	"github.com/cosmoboy/vault/internal/model"
)

// handleRegistry tracks open handles per object, so the workspace knows when a file has a
// writer and can detach handles whose object was replaced or deleted.
type handleRegistry struct {
	mu       sync.Mutex
	byObject map[model.ObjectID]map[*handle]struct{}
}

func newHandleRegistry() *handleRegistry {
	return &handleRegistry{byObject: map[model.ObjectID]map[*handle]struct{}{}}
}

func (r *handleRegistry) add(h *handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.byObject[h.id]
	if set == nil {
		set = map[*handle]struct{}{}
		r.byObject[h.id] = set
	}
	set[h] = struct{}{}
}

func (r *handleRegistry) remove(h *handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byObject[h.id], h)
	if len(r.byObject[h.id]) == 0 {
		delete(r.byObject, h.id)
	}
}

func (r *handleRegistry) hasWriter(id model.ObjectID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.byObject[id] {
		if h.writable && !h.detached.Load() {
			return true
		}
	}
	return false
}

func (r *handleRegistry) detach(id model.ObjectID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.byObject[id] {
		h.detached.Store(true)
	}
}

func (r *handleRegistry) detachAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, set := range r.byObject {
		for h := range set {
			h.detached.Store(true)
		}
	}
}

// rebind moves source's handles to target, for a rename over target.
func (r *handleRegistry) rebind(source, target model.ObjectID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for h := range r.byObject[source] {
		h.id = target
		if r.byObject[target] == nil {
			r.byObject[target] = map[*handle]struct{}{}
		}
		r.byObject[target][h] = struct{}{}
	}
	delete(r.byObject, source)
}

func (r *handleRegistry) objectOf(h *handle) model.ObjectID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return h.id
}
