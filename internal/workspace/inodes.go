package workspace

import (
	"sync"

	"github.com/cosmoboy/vault/internal/model"
)

const rootIno model.Ino = 1

// inodeMap gives each object a stable inode number for the life of the mount. Rename over an
// existing file rebinds the source's inode to the target object, matching what the kernel
// and go-fuse do with their own inode for that name.
type inodeMap struct {
	mu       sync.Mutex
	next     model.Ino
	byObject map[model.ObjectID]model.Ino
	byInode  map[model.Ino]model.ObjectID
}

func newInodeMap() *inodeMap {
	return &inodeMap{
		next:     rootIno + 1,
		byObject: map[model.ObjectID]model.Ino{model.RootID: rootIno},
		byInode:  map[model.Ino]model.ObjectID{rootIno: model.RootID},
	}
}

func (m *inodeMap) inodeOf(id model.ObjectID) model.Ino {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ino, ok := m.byObject[id]; ok {
		return ino
	}
	ino := m.next
	m.next++
	m.byObject[id], m.byInode[ino] = ino, id
	return ino
}

func (m *inodeMap) objectOf(ino model.Ino) (model.ObjectID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byInode[ino]
	return id, ok
}

func (m *inodeMap) existingInode(id model.ObjectID) (model.Ino, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ino, ok := m.byObject[id]
	return ino, ok
}

// rebind points source's inode at target and detaches target's old inode.
func (m *inodeMap) rebind(source, target model.ObjectID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.byObject[target]; ok {
		delete(m.byInode, old)
	}
	sourceIno, ok := m.byObject[source]
	delete(m.byObject, source)
	if !ok {
		delete(m.byObject, target)
		return
	}
	m.byObject[target], m.byInode[sourceIno] = sourceIno, target
}

func (m *inodeMap) forget(id model.ObjectID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ino, ok := m.byObject[id]; ok {
		delete(m.byInode, ino)
		delete(m.byObject, id)
	}
}
