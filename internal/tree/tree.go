// Package tree holds the decrypted folder tree in memory so lookups never touch SQLite or the network.
package tree

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"vault/internal/model"
)

var (
	ErrNotFolder       = errors.New("tree: parent is not a folder")
	ErrMoveIntoSubtree = fmt.Errorf("tree: folder cannot move into its own subtree: %w", fs.ErrInvalid)
	ErrRootImmutable   = fmt.Errorf("tree: root cannot be moved, removed or rebound: %w", fs.ErrInvalid)
	ErrInvalidName     = fmt.Errorf("tree: invalid entry name: %w", fs.ErrInvalid)
)

type node struct {
	entry    model.Entry
	children map[string]model.ObjectID
}

// Tree is safe for concurrent use. It hands out model.Entry copies, never its own nodes.
type Tree struct {
	mu    sync.RWMutex
	nodes map[model.ObjectID]*node
}

func New() *Tree {
	root := &node{
		entry:    model.Entry{ID: model.RootID, Kind: model.KindFolder, State: model.Local},
		children: map[string]model.ObjectID{},
	}
	return &Tree{nodes: map[model.ObjectID]*node{model.RootID: root}}
}

func (t *Tree) Get(id model.ObjectID) (model.Entry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[id]
	if !ok {
		return model.Entry{}, false
	}
	return n.entry, true
}

func (t *Tree) Lookup(parent model.ObjectID, name string) (model.Entry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lookupLocked(parent, name)
}

func (t *Tree) lookupLocked(parent model.ObjectID, name string) (model.Entry, bool) {
	p, ok := t.nodes[parent]
	if !ok {
		return model.Entry{}, false
	}
	id, ok := p.children[name]
	if !ok {
		return model.Entry{}, false
	}
	return t.nodes[id].entry, true
}

// Children returns the folder's entries sorted by name, or nil for a file or unknown id.
func (t *Tree) Children(id model.ObjectID) []model.Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n, ok := t.nodes[id]
	if !ok {
		return nil
	}
	return t.sortedChildrenLocked(n)
}

func (t *Tree) sortedChildrenLocked(n *node) []model.Entry {
	out := make([]model.Entry, 0, len(n.children))
	for _, childID := range n.children {
		out = append(out, t.nodes[childID].entry)
	}
	slices.SortFunc(out, func(a, b model.Entry) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (t *Tree) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.nodes)
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return ErrInvalidName
	}
	return nil
}

func (t *Tree) folderLocked(id model.ObjectID) (*node, error) {
	n, ok := t.nodes[id]
	if !ok {
		return nil, fmt.Errorf("tree: parent %s: %w", id, fs.ErrNotExist)
	}
	if n.entry.Kind != model.KindFolder {
		return nil, fmt.Errorf("tree: parent %s: %w", id, ErrNotFolder)
	}
	return n, nil
}

func newNode(entry model.Entry) *node {
	n := &node{entry: entry}
	if entry.Kind == model.KindFolder {
		n.children = map[string]model.ObjectID{}
	}
	return n
}

// ReplaceContents makes t hold exactly what other holds, so holders of t see the new state.
func (t *Tree) ReplaceContents(other *Tree) {
	other.mu.RLock()
	nodes := other.nodes
	other.mu.RUnlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes = nodes
}
