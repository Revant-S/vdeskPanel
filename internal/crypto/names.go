package crypto

import (
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cosmoboy/vault/internal/model"
)

const MaxNameBytes = 255

var (
	ErrNameTooLong = errors.New("crypto: name longer than 255 bytes")
	ErrInvalidName = errors.New("crypto: invalid file name")
)

// NameCipher encrypts names bound to their object ID, so Drive cannot swap names between objects.
type NameCipher struct {
	aead    cipher.AEAD
	vaultID [16]byte
	random  io.Reader
}

func NewNameCipher(keys *Keys, random io.Reader) *NameCipher {
	return &NameCipher{aead: keys.Name, vaultID: keys.VaultID, random: random}
}

func (c *NameCipher) EncryptName(id model.ObjectID, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	objectID, err := objectIDBytes(id)
	if err != nil {
		return "", err
	}
	sealed, err := Seal(c.aead, c.random, []byte(name), NameAD(c.vaultID, objectID))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *NameCipher) DecryptName(id model.ObjectID, encoded string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode name: %w", err)
	}
	objectID, err := objectIDBytes(id)
	if err != nil {
		return "", err
	}
	plaintext, err := Open(c.aead, sealed, NameAD(c.vaultID, objectID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func ValidateName(name string) error {
	switch {
	case len(name) > MaxNameBytes:
		return ErrNameTooLong
	case name == "", name == ".", name == "..", strings.ContainsAny(name, "/\x00"):
		return ErrInvalidName
	}
	return nil
}

func objectIDBytes(id model.ObjectID) ([16]byte, error) {
	var raw [16]byte
	decoded, err := hex.DecodeString(string(id))
	if err != nil || len(decoded) != len(raw) {
		return raw, fmt.Errorf("object id %q is not 16 hex bytes", id)
	}
	copy(raw[:], decoded)
	return raw, nil
}
