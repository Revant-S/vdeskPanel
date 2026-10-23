package fusefs

import (
	"context"
	"io"
	"io/fs"
	"sort"
	"sync"
	"syscall"
	"time"

	"vault/internal/model"
)

type memoryEntry struct {
	parent     model.Ino
	name       string
	folder     bool
	data       []byte
	executable bool
	modTime    time.Time
}

// memoryStore is the simplest Store that behaves like a POSIX folder, used to test fusefs alone.
type memoryStore struct {
	mu         sync.Mutex
	entries    map[model.Ino]*memoryEntry
	nextIno    model.Ino
	lastCaller Caller
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: map[model.Ino]*memoryEntry{1: {folder: true}}, nextIno: 2}
}

func (s *memoryStore) RootIno() model.Ino { return 1 }

func (s *memoryStore) find(parent model.Ino, name string) (model.Ino, bool) {
	for ino, entry := range s.entries {
		if ino != 1 && entry.parent == parent && entry.name == name {
			return ino, true
		}
	}
	return 0, false
}

func (s *memoryStore) attr(ino model.Ino) Attr {
	e := s.entries[ino]
	return Attr{Ino: ino, Folder: e.folder, Size: int64(len(e.data)), ModTime: e.modTime, Executable: e.executable}
}

func (s *memoryStore) Lookup(_ context.Context, parent model.Ino, name string) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ino, ok := s.find(parent, name)
	if !ok {
		return Attr{}, fs.ErrNotExist
	}
	return s.attr(ino), nil
}

func (s *memoryStore) Getattr(_ context.Context, ino model.Ino) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[ino]; !ok {
		return Attr{}, fs.ErrNotExist
	}
	return s.attr(ino), nil
}

func (s *memoryStore) ReadDir(_ context.Context, ino model.Ino) ([]DirEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []DirEntry
	for child, e := range s.entries {
		if child != 1 && e.parent == ino {
			out = append(out, DirEntry{Name: e.name, Ino: child, Folder: e.folder})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

func (s *memoryStore) add(parent model.Ino, name string, folder bool) (model.Ino, error) {
	if _, ok := s.find(parent, name); ok {
		return 0, fs.ErrExist
	}
	ino := s.nextIno
	s.nextIno++
	s.entries[ino] = &memoryEntry{parent: parent, name: name, folder: folder, modTime: time.Now()}
	return ino, nil
}

func (s *memoryStore) Mkdir(_ context.Context, parent model.Ino, name string) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ino, err := s.add(parent, name, true)
	if err != nil {
		return Attr{}, err
	}
	return s.attr(ino), nil
}

func (s *memoryStore) Create(_ context.Context, parent model.Ino, name string, executable bool, caller Caller) (Attr, Handle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCaller = caller
	ino, err := s.add(parent, name, false)
	if err != nil {
		return Attr{}, nil, err
	}
	s.entries[ino].executable = executable
	return s.attr(ino), &memoryHandle{store: s, ino: ino}, nil
}

func (s *memoryStore) Open(_ context.Context, ino model.Ino, _, truncate bool, caller Caller) (Handle, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCaller = caller
	if truncate {
		s.entries[ino].data = nil
	}
	return &memoryHandle{store: s, ino: ino}, true, nil
}

func (s *memoryStore) Rename(_ context.Context, parent model.Ino, name string, newParent model.Ino, newName string, noReplace bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ino, ok := s.find(parent, name)
	if !ok {
		return fs.ErrNotExist
	}
	if existing, ok := s.find(newParent, newName); ok {
		if noReplace {
			return fs.ErrExist
		}
		delete(s.entries, existing)
	}
	s.entries[ino].parent, s.entries[ino].name = newParent, newName
	return nil
}

func (s *memoryStore) Unlink(_ context.Context, parent model.Ino, name string, _ Caller) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ino, ok := s.find(parent, name)
	if !ok {
		return fs.ErrNotExist
	}
	delete(s.entries, ino)
	return nil
}

func (s *memoryStore) Rmdir(_ context.Context, parent model.Ino, name string, _ Caller) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ino, ok := s.find(parent, name)
	if !ok {
		return fs.ErrNotExist
	}
	for _, e := range s.entries {
		if e.parent == ino {
			return syscall.ENOTEMPTY
		}
	}
	delete(s.entries, ino)
	return nil
}

func (s *memoryStore) Setattr(_ context.Context, ino model.Ino, change AttrChange) (Attr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[ino]
	if change.Size != nil {
		data := make([]byte, *change.Size)
		copy(data, e.data)
		e.data = data
	}
	if change.ModTime != nil {
		e.modTime = *change.ModTime
	}
	if change.Executable != nil {
		e.executable = *change.Executable
	}
	return s.attr(ino), nil
}

func (s *memoryStore) StatFS(context.Context) (StatFS, error) {
	return StatFS{TotalBytes: 1 << 30, FreeBytes: 1 << 29, AvailableBytes: 1 << 29}, nil
}

func (s *memoryStore) replaceContent(ino model.Ino, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[ino].data = data
}

type memoryHandle struct {
	store *memoryStore
	ino   model.Ino
}

func (h *memoryHandle) ReadAt(_ context.Context, p []byte, offset int64) (int, error) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	data := h.store.entries[h.ino].data
	if offset >= int64(len(data)) {
		return 0, io.EOF
	}
	return copy(p, data[offset:]), nil
}

func (h *memoryHandle) WriteAt(_ context.Context, p []byte, offset int64) (int, error) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	e := h.store.entries[h.ino]
	if end := int(offset) + len(p); end > len(e.data) {
		e.data = append(e.data, make([]byte, end-len(e.data))...)
	}
	return copy(e.data[offset:], p), nil
}

func (h *memoryHandle) Sync(context.Context) error    { return nil }
func (h *memoryHandle) Release(context.Context) error { return nil }
