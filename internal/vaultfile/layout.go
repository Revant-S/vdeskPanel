// Package vaultfile reads and writes VLT1 bodies: a fixed header followed by independently
// sealed 64 KiB segments, so any byte range can be read or rewritten on its own.
package vaultfile

import (
	"errors"
	"fmt"

	"vault/internal/crypto"
)

const (
	SegmentPlaintextSize  = 64 << 10
	SegmentOverhead       = crypto.Overhead
	SegmentCiphertextSize = SegmentPlaintextSize + SegmentOverhead
	HeaderSize            = 128
)

var ErrCorruptLayout = errors.New("vaultfile: ciphertext size does not match the segment layout")

// SegmentCount is never zero: an empty body still holds one sealed final segment, so
// truncation to zero is detectable.
func SegmentCount(plaintextSize int64) int64 {
	if plaintextSize <= 0 {
		return 1
	}
	return (plaintextSize + SegmentPlaintextSize - 1) / SegmentPlaintextSize
}

func SegmentOffset(index int64) int64 {
	return HeaderSize + index*SegmentCiphertextSize
}

func segmentPlaintextLength(index, plaintextSize int64) int64 {
	if index < SegmentCount(plaintextSize)-1 {
		return SegmentPlaintextSize
	}
	return plaintextSize - index*SegmentPlaintextSize
}

func CiphertextSize(plaintextSize int64) int64 {
	last := SegmentCount(plaintextSize) - 1
	return SegmentOffset(last) + segmentPlaintextLength(last, plaintextSize) + SegmentOverhead
}

func PlaintextSize(ciphertextSize int64) (int64, error) {
	rest := ciphertextSize - HeaderSize
	if rest < SegmentOverhead {
		return 0, fmt.Errorf("%w: %d bytes", ErrCorruptLayout, ciphertextSize)
	}
	full, remainder := rest/SegmentCiphertextSize, rest%SegmentCiphertextSize
	switch {
	case remainder == 0:
		return full * SegmentPlaintextSize, nil
	case remainder < SegmentOverhead:
		return 0, fmt.Errorf("%w: %d bytes", ErrCorruptLayout, ciphertextSize)
	default:
		return full*SegmentPlaintextSize + remainder - SegmentOverhead, nil
	}
}

// SegmentSpan returns the first and last segment indexes touched by a plaintext range.
func SegmentSpan(offset, length int64) (first, last int64) {
	if length <= 0 {
		return offset / SegmentPlaintextSize, offset / SegmentPlaintextSize
	}
	return offset / SegmentPlaintextSize, (offset + length - 1) / SegmentPlaintextSize
}
