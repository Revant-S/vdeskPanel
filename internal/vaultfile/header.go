package vaultfile

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/secret"
)

const FormatVersion = 1

var (
	magic                = []byte("VLT1")
	ErrNotVaultFile      = errors.New("vaultfile: missing VLT1 magic")
	ErrUnsupportedFormat = errors.New("vaultfile: body format is newer than this build")
)

const wrappedFileKeySize = crypto.Overhead + crypto.KeySize

type BodyID [16]byte

type Header struct {
	Version        uint16
	BodyID         BodyID
	WrappedFileKey []byte
}

func (h Header) marshal() []byte {
	out := make([]byte, HeaderSize)
	copy(out, magic)
	binary.BigEndian.PutUint16(out[4:], h.Version)
	copy(out[6:22], h.BodyID[:])
	copy(out[22:22+wrappedFileKeySize], h.WrappedFileKey)
	return out
}

func ParseHeader(data []byte) (Header, error) {
	if len(data) < HeaderSize || !bytes.Equal(data[:4], magic) {
		return Header{}, ErrNotVaultFile
	}
	h := Header{Version: binary.BigEndian.Uint16(data[4:])}
	if h.Version > FormatVersion {
		return Header{}, fmt.Errorf("body format %d: %w", h.Version, ErrUnsupportedFormat)
	}
	copy(h.BodyID[:], data[6:22])
	h.WrappedFileKey = append([]byte(nil), data[22:22+wrappedFileKeySize]...)
	return h, nil
}

func ReadHeader(r io.ReaderAt) (Header, error) {
	data := make([]byte, HeaderSize)
	if _, err := r.ReadAt(data, 0); err != nil {
		return Header{}, fmt.Errorf("read body header: %w", err)
	}
	return ParseHeader(data)
}

func newHeader(content cipher.AEAD, random io.Reader) (Header, *secret.Key, error) {
	h := Header{Version: FormatVersion}
	if _, err := io.ReadFull(random, h.BodyID[:]); err != nil {
		return Header{}, nil, fmt.Errorf("read body id: %w", err)
	}
	fileKey, err := secret.RandomKey(crypto.KeySize)
	if err != nil {
		return Header{}, nil, err
	}
	h.WrappedFileKey, err = crypto.WrapKey(content, random, fileKey, crypto.FileKeyAD(h.Version, h.BodyID))
	if err != nil {
		fileKey.Destroy()
		return Header{}, nil, err
	}
	return h, fileKey, nil
}

func (h Header) unwrapFileKey(content cipher.AEAD) (*secret.Key, error) {
	key, err := crypto.UnwrapKey(content, h.WrappedFileKey, crypto.FileKeyAD(h.Version, h.BodyID))
	if err != nil {
		return nil, fmt.Errorf("unwrap file key: %w", ErrTampered)
	}
	return key, nil
}
