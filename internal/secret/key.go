// Package secret keeps key material in locked, guarded memory and wipes it when done.
package secret

import (
	"errors"

	"github.com/awnumar/memguard"
)

var ErrLockedMemory = errors.New("secret: could not allocate locked memory")

type Key struct {
	buffer *memguard.LockedBuffer
}

// NewKey moves material into locked memory and wipes the source slice.
func NewKey(material []byte) (*Key, error) {
	defer Wipe(material)
	buffer := memguard.NewBufferFromBytes(material)
	if buffer.Size() == 0 {
		return nil, ErrLockedMemory
	}
	return &Key{buffer: buffer}, nil
}

func RandomKey(size int) (*Key, error) {
	buffer := memguard.NewBufferRandom(size)
	if buffer.Size() == 0 {
		return nil, ErrLockedMemory
	}
	return &Key{buffer: buffer}, nil
}

// Bytes is a view into locked memory and must not be used after Destroy.
func (k *Key) Bytes() []byte {
	return k.buffer.Bytes()
}

func (k *Key) Alive() bool {
	return k != nil && k.buffer.IsAlive()
}

func (k *Key) Destroy() {
	if k != nil {
		k.buffer.Destroy()
	}
}

func Wipe(b []byte) {
	memguard.WipeBytes(b)
}
