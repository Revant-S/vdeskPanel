package auth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/secret"
	"golang.org/x/oauth2"
)

var ErrNoToken = errors.New("auth: no saved Google token")

// TokenStore keeps the OAuth token sealed with the token subkey, so Drive access also
// needs the vault password (spec §4).
type TokenStore struct {
	path    string
	keyring *crypto.Keyring
}

func NewTokenStore(path string, keyring *crypto.Keyring) *TokenStore {
	return &TokenStore{path: path, keyring: keyring}
}

func (s *TokenStore) Save(token *oauth2.Token) error {
	plaintext, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	defer secret.Wipe(plaintext)
	var sealed []byte
	err = s.keyring.Use(func(keys *crypto.Keys) error {
		var err error
		sealed, err = crypto.Seal(keys.Token, rand.Reader, plaintext, crypto.TokenAD(keys.VaultID))
		return err
	})
	if err != nil {
		return fmt.Errorf("seal token: %w", err)
	}
	return writeFileAtomically(s.path, sealed)
}

func (s *TokenStore) Load() (*oauth2.Token, error) {
	sealed, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}
	var plaintext []byte
	err = s.keyring.Use(func(keys *crypto.Keys) error {
		var err error
		plaintext, err = crypto.Open(keys.Token, sealed, crypto.TokenAD(keys.VaultID))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("open token: %w", err)
	}
	defer secret.Wipe(plaintext)
	var token oauth2.Token
	if err := json.Unmarshal(plaintext, &token); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	return &token, nil
}

// writeFileAtomically never leaves a half-written token: a crash keeps either the old
// file or the new one.
func writeFileAtomically(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(temporary.Name())
		}
	}()
	if err := writeAndSync(temporary, data); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}
	renamed = true
	return syncDirectory(directory)
}

// writeAndSync relies on os.CreateTemp creating the file with mode 0600.
func writeAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write token file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync token file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close token file: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open token directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync token directory: %w", err)
	}
	return nil
}
