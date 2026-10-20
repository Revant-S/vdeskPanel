package crypto

import (
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cosmoboy/vault/internal/secret"
	"golang.org/x/crypto/hkdf"
)

var (
	ErrWrongPassword = errors.New("crypto: wrong password")
	ErrLocked        = errors.New("crypto: keyring is locked")
)

// Keys are the per-purpose subkeys derived from the vault master key.
type Keys struct {
	VaultID   [16]byte
	Name      cipher.AEAD
	Content   cipher.AEAD
	Log       cipher.AEAD
	Token     cipher.AEAD
	IndexSeal cipher.AEAD
	IndexMAC  *secret.Key
}

// Keyring guards Keys so that Destroy waits for every in-flight use to finish.
type Keyring struct {
	mu   sync.RWMutex
	keys *Keys
}

func (r *Keyring) Use(fn func(*Keys) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.keys == nil {
		return ErrLocked
	}
	return fn(r.keys)
}

func (r *Keyring) Destroy() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys != nil {
		r.keys.IndexMAC.Destroy()
		r.keys = nil
	}
}

// CreateVault builds a new key hierarchy. It wipes password before returning.
func CreateVault(password []byte, params KDFParams, random io.Reader) (VaultHeader, *Keyring, error) {
	defer secret.Wipe(password)
	header := VaultHeader{FormatVersion: CurrentFormatVersion, KDF: params}
	if _, err := io.ReadFull(random, header.VaultID[:]); err != nil {
		return VaultHeader{}, nil, fmt.Errorf("read vault id: %w", err)
	}
	if _, err := io.ReadFull(random, header.Salt[:]); err != nil {
		return VaultHeader{}, nil, fmt.Errorf("read salt: %w", err)
	}
	masterMaterial := make([]byte, KeySize)
	if _, err := io.ReadFull(random, masterMaterial); err != nil {
		return VaultHeader{}, nil, fmt.Errorf("read master key: %w", err)
	}
	master, err := secret.NewKey(masterMaterial)
	if err != nil {
		return VaultHeader{}, nil, err
	}
	defer master.Destroy()
	wrapping, err := keyEncryptionCipher(password, header)
	if err != nil {
		return VaultHeader{}, nil, err
	}
	header.WrappedMasterKey, err = WrapKey(wrapping, random, master, HeaderAD(header.VaultID))
	if err != nil {
		return VaultHeader{}, nil, err
	}
	keyring, err := deriveKeyring(master, header.VaultID)
	return header, keyring, err
}

// UnlockVault unwraps the master key with password and derives the subkeys. It wipes password.
func UnlockVault(header VaultHeader, password []byte) (*Keyring, error) {
	defer secret.Wipe(password)
	wrapping, err := keyEncryptionCipher(password, header)
	if err != nil {
		return nil, err
	}
	master, err := UnwrapKey(wrapping, header.WrappedMasterKey, HeaderAD(header.VaultID))
	if errors.Is(err, ErrDecrypt) {
		return nil, ErrWrongPassword
	}
	if err != nil {
		return nil, err
	}
	defer master.Destroy()
	return deriveKeyring(master, header.VaultID)
}

func keyEncryptionCipher(password []byte, header VaultHeader) (cipher.AEAD, error) {
	if err := header.KDF.Validate(); err != nil {
		return nil, err
	}
	kek, err := deriveKeyEncryptionKey(password, header.Salt[:], header.KDF)
	if err != nil {
		return nil, err
	}
	defer kek.Destroy()
	return NewAEAD(kek)
}

func deriveKeyring(master *secret.Key, vaultID [16]byte) (*Keyring, error) {
	keys := &Keys{VaultID: vaultID}
	ciphers := []struct {
		purpose string
		target  *cipher.AEAD
	}{
		{"name", &keys.Name},
		{"content", &keys.Content},
		{"log", &keys.Log},
		{"token", &keys.Token},
		{"index/seal", &keys.IndexSeal},
	}
	for _, c := range ciphers {
		subkey, err := deriveSubkey(master, vaultID, c.purpose)
		if err != nil {
			return nil, err
		}
		*c.target, err = NewAEAD(subkey)
		subkey.Destroy()
		if err != nil {
			return nil, err
		}
	}
	mac, err := deriveSubkey(master, vaultID, "index/mac")
	if err != nil {
		return nil, err
	}
	keys.IndexMAC = mac
	return &Keyring{keys: keys}, nil
}

func deriveSubkey(master *secret.Key, vaultID [16]byte, purpose string) (*secret.Key, error) {
	material := make([]byte, KeySize)
	reader := hkdf.New(sha256.New, master.Bytes(), vaultID[:], []byte("vault/v1/"+purpose))
	if _, err := io.ReadFull(reader, material); err != nil {
		return nil, fmt.Errorf("derive %s subkey: %w", purpose, err)
	}
	return secret.NewKey(material)
}
