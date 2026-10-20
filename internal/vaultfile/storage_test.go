package vaultfile

import (
	"crypto/cipher"
	"crypto/rand"
	"io"
	"sync"
	"testing"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/secret"
)

type memoryStorage struct {
	mu   sync.Mutex
	data []byte
}

func (m *memoryStorage) ReadAt(p []byte, offset int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if offset >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *memoryStorage) WriteAt(p []byte, offset int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if end := offset + int64(len(p)); end > int64(len(m.data)) {
		m.data = append(m.data, make([]byte, end-int64(len(m.data)))...)
	}
	return copy(m.data[offset:], p), nil
}

func (m *memoryStorage) Truncate(size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if size <= int64(len(m.data)) {
		m.data = m.data[:size]
	} else {
		m.data = append(m.data, make([]byte, size-int64(len(m.data)))...)
	}
	return nil
}

func (m *memoryStorage) bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data...)
}

func testContentKey(t testing.TB) cipher.AEAD {
	t.Helper()
	key, err := secret.RandomKey(crypto.KeySize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Destroy)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func newTestBody(t testing.TB, content cipher.AEAD) (*Body, *memoryStorage) {
	t.Helper()
	storage := &memoryStorage{}
	body, err := NewBody(storage, content, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(body.Close)
	return body, storage
}

func readAll(t testing.TB, body *Body) []byte {
	t.Helper()
	out := make([]byte, body.Size())
	n, err := body.ReadAt(out, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	return out[:n]
}
