package fusefs

import "github.com/cosmoboy/vault/internal/model"

type MemoryStore = memoryStore

func NewMemoryStore() *MemoryStore { return newMemoryStore() }

func (s *memoryStore) LastCaller() Caller {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCaller
}

func (s *memoryStore) ReplaceContent(ino model.Ino, data []byte) {
	s.replaceContent(ino, data)
}
