// Package crypto implements the vault key hierarchy and the XChaCha20-Poly1305 envelopes built on it.
package crypto

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"vault/internal/secret"
)

const (
	KeySize   = chacha20poly1305.KeySize
	NonceSize = chacha20poly1305.NonceSizeX
	TagSize   = chacha20poly1305.Overhead
	Overhead  = NonceSize + TagSize
)

var ErrDecrypt = errors.New("crypto: message failed authentication")

// NewAEAD builds a cipher from a locked key. The cipher keeps its own heap copy of the key,
// so long-lived ciphers are built once per key rather than per message.
func NewAEAD(key *secret.Key) (cipher.AEAD, error) {
	aead, err := chacha20poly1305.NewX(key.Bytes())
	if err != nil {
		return nil, fmt.Errorf("build xchacha20poly1305: %w", err)
	}
	return aead, nil
}

// Seal returns nonce || ciphertext || tag.
func Seal(aead cipher.AEAD, random io.Reader, plaintext, additionalData []byte) ([]byte, error) {
	out := make([]byte, NonceSize, NonceSize+len(plaintext)+TagSize)
	if _, err := io.ReadFull(random, out); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return aead.Seal(out, out, plaintext, additionalData), nil
}

func Open(aead cipher.AEAD, sealed, additionalData []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, ErrDecrypt
	}
	plaintext, err := aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], additionalData)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func WrapKey(wrapping cipher.AEAD, random io.Reader, key *secret.Key, additionalData []byte) ([]byte, error) {
	return Seal(wrapping, random, key.Bytes(), additionalData)
}

func UnwrapKey(wrapping cipher.AEAD, wrapped, additionalData []byte) (*secret.Key, error) {
	material, err := Open(wrapping, wrapped, additionalData)
	if err != nil {
		return nil, err
	}
	return secret.NewKey(material)
}
