package vaultfile

import (
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/crypto"
)

type ResealResult struct {
	Header          Header
	PlaintextSize   int64
	PlaintextSHA256 [32]byte
	CiphertextMD5   [16]byte
}

// Reseal copies src into dst under a fresh file key and BodyID, authenticating every source
// segment on the way. The MD5 is what Drive will report for dst once uploaded.
func Reseal(src *Body, dst Storage, content cipher.AEAD, random io.Reader) (ResealResult, error) {
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.aead == nil {
		return ResealResult{}, ErrClosed
	}
	header, fileKey, err := newHeader(content, random)
	if err != nil {
		return ResealResult{}, err
	}
	out, err := newBodyWithKey(dst, header, fileKey, random, src.size)
	if err != nil {
		return ResealResult{}, err
	}
	defer out.Close()

	plaintextHash, ciphertextHash := sha256.New(), md5.New()
	headerBytes := header.marshal()
	if _, err := dst.WriteAt(headerBytes, 0); err != nil {
		return ResealResult{}, fmt.Errorf("write resealed header: %w", err)
	}
	ciphertextHash.Write(headerBytes)
	count := SegmentCount(src.size)
	for index := int64(0); index < count; index++ {
		plaintext, err := src.readSegment(index)
		if err != nil {
			return ResealResult{}, err
		}
		plaintextHash.Write(plaintext)
		sealed, err := crypto.Seal(out.aead, random, plaintext, crypto.SegmentAD(header.BodyID, uint64(index), index == count-1))
		if err != nil {
			return ResealResult{}, err
		}
		if _, err := dst.WriteAt(sealed, SegmentOffset(index)); err != nil {
			return ResealResult{}, fmt.Errorf("write resealed segment %d: %w", index, err)
		}
		ciphertextHash.Write(sealed)
	}
	if err := dst.Truncate(CiphertextSize(src.size)); err != nil {
		return ResealResult{}, fmt.Errorf("truncate resealed body: %w", err)
	}
	result := ResealResult{Header: header, PlaintextSize: src.size}
	copy(result.PlaintextSHA256[:], plaintextHash.Sum(nil))
	copy(result.CiphertextMD5[:], ciphertextHash.Sum(nil))
	return result, nil
}

// VerifyBody authenticates a complete ciphertext body and returns its plaintext SHA-256.
func VerifyBody(r io.ReaderAt, ciphertextSize int64, content cipher.AEAD) (Header, [32]byte, error) {
	plaintextSize, err := PlaintextSize(ciphertextSize)
	if err != nil {
		return Header{}, [32]byte{}, err
	}
	body, err := OpenBody(&readOnlyStorage{r}, content, plaintextSize, nil)
	if err != nil {
		return Header{}, [32]byte{}, err
	}
	defer body.Close()
	hash := sha256.New()
	body.mu.Lock()
	defer body.mu.Unlock()
	for index := int64(0); index < SegmentCount(plaintextSize); index++ {
		plaintext, err := body.readSegment(index)
		if err != nil {
			return Header{}, [32]byte{}, err
		}
		hash.Write(plaintext)
	}
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return body.header, sum, nil
}

type readOnlyStorage struct {
	io.ReaderAt
}

func (*readOnlyStorage) WriteAt([]byte, int64) (int, error) {
	return 0, fmt.Errorf("vaultfile: storage is read-only")
}

func (*readOnlyStorage) Truncate(int64) error {
	return fmt.Errorf("vaultfile: storage is read-only")
}
