package vaultfile

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"sync"

	"vault/internal/crypto"
	"vault/internal/secret"
)

var (
	ErrTampered = errors.New("vaultfile: body failed authentication")
	ErrClosed   = errors.New("vaultfile: body is closed")
)

type SegmentError struct {
	Index int64
}

func (e *SegmentError) Error() string {
	return fmt.Sprintf("vaultfile: segment %d failed authentication", e.Index)
}

func (e *SegmentError) Unwrap() error {
	return ErrTampered
}

type Storage interface {
	io.ReaderAt
	io.WriterAt
	Truncate(size int64) error
}

// Body is one immutable-identity VLT1 body: its file key and BodyID never change, and every
// segment rewrite uses a fresh nonce.
type Body struct {
	mu      sync.Mutex
	storage Storage
	header  Header
	fileKey *secret.Key
	aead    cipher.AEAD
	random  io.Reader
	size    int64
}

func NewBody(storage Storage, content cipher.AEAD, random io.Reader) (*Body, error) {
	header, fileKey, err := newHeader(content, random)
	if err != nil {
		return nil, err
	}
	body, err := newBodyWithKey(storage, header, fileKey, random, 0)
	if err != nil {
		return nil, err
	}
	if _, err := storage.WriteAt(header.marshal(), 0); err != nil {
		body.Close()
		return nil, fmt.Errorf("write body header: %w", err)
	}
	if err := body.writeSegment(0, nil, true); err != nil {
		body.Close()
		return nil, err
	}
	if err := storage.Truncate(CiphertextSize(0)); err != nil {
		body.Close()
		return nil, fmt.Errorf("truncate new body: %w", err)
	}
	return body, nil
}

func OpenBody(storage Storage, content cipher.AEAD, plaintextSize int64, random io.Reader) (*Body, error) {
	header, err := ReadHeader(storage)
	if err != nil {
		return nil, err
	}
	fileKey, err := header.unwrapFileKey(content)
	if err != nil {
		return nil, err
	}
	return newBodyWithKey(storage, header, fileKey, random, plaintextSize)
}

func newBodyWithKey(storage Storage, header Header, fileKey *secret.Key, random io.Reader, size int64) (*Body, error) {
	aead, err := crypto.NewAEAD(fileKey)
	if err != nil {
		fileKey.Destroy()
		return nil, err
	}
	return &Body{storage: storage, header: header, fileKey: fileKey, aead: aead, random: random, size: size}, nil
}

func (b *Body) Header() Header {
	return b.header
}

func (b *Body) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

func (b *Body) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fileKey.Destroy()
	b.aead = nil
}

func (b *Body) ReadAt(p []byte, offset int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aead == nil {
		return 0, ErrClosed
	}
	if offset >= b.size {
		return 0, io.EOF
	}
	end := min(offset+int64(len(p)), b.size)
	first, last := SegmentSpan(offset, end-offset)
	read := 0
	for index := first; index <= last; index++ {
		plaintext, err := b.readSegment(index)
		if err != nil {
			return read, err
		}
		start := max(offset-index*SegmentPlaintextSize, 0)
		read += copy(p[read:], plaintext[start:])
	}
	if read < len(p) {
		return read, io.EOF
	}
	return read, nil
}

func (b *Body) WriteAt(p []byte, offset int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aead == nil {
		return 0, ErrClosed
	}
	newSize := max(b.size, offset+int64(len(p)))
	first := min(offset/SegmentPlaintextSize, SegmentCount(b.size)-1)
	_, last := SegmentSpan(offset, int64(len(p)))
	err := b.rewriteSegments(first, last, newSize, func(index int64, plaintext []byte) {
		segmentStart := index * SegmentPlaintextSize
		from := max(offset-segmentStart, 0)
		source := max(segmentStart-offset, 0)
		if source < int64(len(p)) && from < int64(len(plaintext)) {
			copy(plaintext[from:], p[source:])
		}
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (b *Body) Truncate(size int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aead == nil {
		return ErrClosed
	}
	if size == b.size {
		return nil
	}
	newLast := SegmentCount(size) - 1
	first := min(newLast, SegmentCount(b.size)-1)
	if err := b.rewriteSegments(first, newLast, size, func(int64, []byte) {}); err != nil {
		return err
	}
	if err := b.storage.Truncate(CiphertextSize(size)); err != nil {
		return fmt.Errorf("truncate body: %w", err)
	}
	return nil
}

// rewriteSegments re-seals segments first..last for a body of newSize, carrying over existing
// plaintext, zero-filling growth, and letting patch overwrite bytes before sealing.
func (b *Body) rewriteSegments(first, last, newSize int64, patch func(index int64, plaintext []byte)) error {
	oldCount := SegmentCount(b.size)
	newCount := SegmentCount(newSize)
	for index := first; index <= last; index++ {
		plaintext := make([]byte, segmentPlaintextLength(index, newSize))
		if index < oldCount {
			existing, err := b.readSegment(index)
			if err != nil {
				return err
			}
			copy(plaintext, existing)
		}
		patch(index, plaintext)
		if err := b.writeSegment(index, plaintext, index == newCount-1); err != nil {
			return err
		}
	}
	b.size = newSize
	return nil
}

// RecoverInterruptedRewrite repairs what a rewrite killed midway leaves behind, given the size
// the file's length implies. A body that grew only partway ends in a full segment still
// sealed as non-final: it is resealed as final, keeping the bytes that reached disk. A
// truncate to zero that wrote the empty final segment but never shortened the file is
// finished. Anything else is left as it is.
func (b *Body) RecoverInterruptedRewrite() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aead == nil {
		return false, ErrClosed
	}
	if b.size > 0 {
		if _, err := b.openSegment(0, 0, true); err == nil {
			if err := b.storage.Truncate(CiphertextSize(0)); err != nil {
				return false, fmt.Errorf("finish truncate: %w", err)
			}
			b.size = 0
			return true, nil
		}
	}
	last := SegmentCount(b.size) - 1
	length := segmentPlaintextLength(last, b.size)
	if length != SegmentPlaintextSize {
		return false, nil
	}
	if _, err := b.openSegment(last, length, true); err == nil {
		return false, nil
	}
	plaintext, err := b.openSegment(last, length, false)
	if err != nil {
		return false, nil
	}
	return true, b.writeSegment(last, plaintext, true)
}

// FinishInterruptedShrink completes a truncate to target that a crash cut off between
// writing the new final segment and shortening the file. If that segment was never written,
// the old body is still whole and is left as it is.
func (b *Body) FinishInterruptedShrink(target int64) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aead == nil {
		return false, ErrClosed
	}
	if target <= 0 || target >= b.size {
		return false, nil
	}
	last := SegmentCount(target) - 1
	if _, err := b.openSegment(last, segmentPlaintextLength(last, target), true); err != nil {
		return false, nil
	}
	if err := b.storage.Truncate(CiphertextSize(target)); err != nil {
		return false, fmt.Errorf("finish shrink: %w", err)
	}
	b.size = target
	return true, nil
}

func (b *Body) readSegment(index int64) ([]byte, error) {
	return b.openSegment(index, segmentPlaintextLength(index, b.size), index == SegmentCount(b.size)-1)
}

func (b *Body) openSegment(index, length int64, final bool) ([]byte, error) {
	sealed := make([]byte, length+SegmentOverhead)
	if _, err := b.storage.ReadAt(sealed, SegmentOffset(index)); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read segment %d: %w", index, err)
	}
	plaintext, err := crypto.Open(b.aead, sealed, crypto.SegmentAD(b.header.BodyID, uint64(index), final))
	if err != nil {
		return nil, &SegmentError{Index: index}
	}
	return plaintext, nil
}

func (b *Body) writeSegment(index int64, plaintext []byte, final bool) error {
	sealed, err := crypto.Seal(b.aead, b.random, plaintext, crypto.SegmentAD(b.header.BodyID, uint64(index), final))
	if err != nil {
		return err
	}
	if _, err := b.storage.WriteAt(sealed, SegmentOffset(index)); err != nil {
		return fmt.Errorf("write segment %d: %w", index, err)
	}
	return nil
}
