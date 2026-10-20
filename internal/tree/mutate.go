package tree

import (
	"fmt"
	"io/fs"

	"github.com/cosmoboy/vault/internal/model"
)

var ErrPlacementChanged = fmt.Errorf("tree: Update cannot change parent, name or kind; use Move: %w", fs.ErrInvalid)

func (t *Tree) Insert(entry model.Entry) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.insertLocked(entry)
}

func (t *Tree) insertLocked(entry model.Entry) error {
	if err := validateName(entry.Name); err != nil {
		return err
	}
	if _, exists := t.nodes[entry.ID]; exists {
		return fmt.Errorf("tree: insert %s: %w", entry.ID, fs.ErrExist)
	}
	parent, err := t.folderLocked(entry.ParentID)
	if err != nil {
		return fmt.Errorf("tree: insert %s: %w", entry.ID, err)
	}
	if _, taken := parent.children[entry.Name]; taken {
		return fmt.Errorf("tree: insert %s: name taken in %s: %w", entry.ID, entry.ParentID, fs.ErrExist)
	}
	t.nodes[entry.ID] = newNode(entry)
	parent.children[entry.Name] = entry.ID
	return nil
}

// Update replaces an entry's metadata (size, mtime, state, flags). Placement changes go through Move.
func (t *Tree) Update(entry model.Entry) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, ok := t.nodes[entry.ID]
	if !ok {
		return fmt.Errorf("tree: update %s: %w", entry.ID, fs.ErrNotExist)
	}
	current := n.entry
	if entry.ParentID != current.ParentID || entry.Name != current.Name || entry.Kind != current.Kind {
		return fmt.Errorf("tree: update %s: %w", entry.ID, ErrPlacementChanged)
	}
	n.entry = entry
	return nil
}

// Move refuses an occupied target name; a rename-over must Remove the target first.
func (t *Tree) Move(id, newParent model.ObjectID, newName string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == model.RootID {
		return ErrRootImmutable
	}
	if err := validateName(newName); err != nil {
		return err
	}
	n, ok := t.nodes[id]
	if !ok {
		return fmt.Errorf("tree: move %s: %w", id, fs.ErrNotExist)
	}
	target, err := t.folderLocked(newParent)
	if err != nil {
		return fmt.Errorf("tree: move %s: %w", id, err)
	}
	if t.isWithinLocked(newParent, id) {
		return fmt.Errorf("tree: move %s: %w", id, ErrMoveIntoSubtree)
	}
	if occupant, taken := target.children[newName]; taken {
		if occupant == id {
			return nil
		}
		return fmt.Errorf("tree: move %s: name taken in %s: %w", id, newParent, fs.ErrExist)
	}
	delete(t.nodes[n.entry.ParentID].children, n.entry.Name)
	target.children[newName] = id
	n.entry.ParentID, n.entry.Name = newParent, newName
	return nil
}

// isWithinLocked reports whether id is ancestor or id itself.
func (t *Tree) isWithinLocked(id, ancestor model.ObjectID) bool {
	for current := id; ; {
		if current == ancestor {
			return true
		}
		if current == model.RootID {
			return false
		}
		current = t.nodes[current].entry.ParentID
	}
}

// Remove detaches id and its whole subtree, returning the removed entries with id first and
// each folder before its children, siblings in name order.
func (t *Tree) Remove(id model.ObjectID) ([]model.Entry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == model.RootID {
		return nil, ErrRootImmutable
	}
	n, ok := t.nodes[id]
	if !ok {
		return nil, fmt.Errorf("tree: remove %s: %w", id, fs.ErrNotExist)
	}
	removed := t.collectSubtreeLocked(n, nil)
	for _, entry := range removed {
		delete(t.nodes, entry.ID)
	}
	delete(t.nodes[n.entry.ParentID].children, n.entry.Name)
	return removed, nil
}

func (t *Tree) collectSubtreeLocked(n *node, out []model.Entry) []model.Entry {
	out = append(out, n.entry)
	for _, child := range t.sortedChildrenLocked(n) {
		out = t.collectSubtreeLocked(t.nodes[child.ID], out)
	}
	return out
}

// Rebind gives an entry a new object ID while it keeps its place, as when a never-synced file
// adopts the identity of the file it replaced (arbitration R9, R10).
func (t *Tree) Rebind(oldID, newID model.ObjectID) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if oldID == model.RootID || newID == model.RootID {
		return ErrRootImmutable
	}
	n, ok := t.nodes[oldID]
	if !ok {
		return fmt.Errorf("tree: rebind %s: %w", oldID, fs.ErrNotExist)
	}
	if _, exists := t.nodes[newID]; exists {
		return fmt.Errorf("tree: rebind to %s: %w", newID, fs.ErrExist)
	}
	delete(t.nodes, oldID)
	n.entry.ID = newID
	t.nodes[newID] = n
	t.nodes[n.entry.ParentID].children[n.entry.Name] = newID
	for _, childID := range n.children {
		t.nodes[childID].entry.ParentID = newID
	}
	return nil
}
