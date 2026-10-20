package tree

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/cosmoboy/vault/internal/model"
)

// PathOf returns the slash path from the root, "/" for the root itself.
func (t *Tree) PathOf(id model.ObjectID) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var names []string
	for current := id; current != model.RootID; {
		n, ok := t.nodes[current]
		if !ok {
			return "", fmt.Errorf("tree: path of %s: %w", id, fs.ErrNotExist)
		}
		names = append(names, n.entry.Name)
		current = n.entry.ParentID
	}
	slices.Reverse(names)
	return "/" + strings.Join(names, "/"), nil
}

// Resolve accepts an absolute slash path; empty segments are ignored, "." and ".." are not.
func (t *Tree) Resolve(path string) (model.Entry, bool) {
	if !strings.HasPrefix(path, "/") {
		return model.Entry{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	current := t.nodes[model.RootID].entry
	for _, name := range strings.Split(path, "/") {
		if name == "" {
			continue
		}
		next, ok := t.lookupLocked(current.ID, name)
		if !ok {
			return model.Entry{}, false
		}
		current = next
	}
	return current, true
}

// Aggregate sums file sizes under id. Local counts Local and Dirty files in full; a Partial
// file counts as zero local bytes because the tree does not know which segments are present.
func (t *Tree) Aggregate(id model.ObjectID) (total, local int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[id]
	if !ok {
		return 0, 0
	}
	stack := []*node{n}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.entry.Kind == model.KindFile {
			total += current.entry.Size
			if current.entry.State == model.Local || current.entry.State == model.Dirty {
				local += current.entry.Size
			}
		}
		for _, childID := range current.children {
			stack = append(stack, t.nodes[childID])
		}
	}
	return total, local
}
