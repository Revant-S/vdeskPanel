package crypto

import (
	"io"

	"vault/internal/model"
)

// KeyringNames encrypts and decrypts names under the keyring's read lock, so a lock cannot
// destroy keys mid-call.
type KeyringNames struct {
	keyring *Keyring
	random  io.Reader
}

func NewKeyringNames(keyring *Keyring, random io.Reader) *KeyringNames {
	return &KeyringNames{keyring: keyring, random: random}
}

func (n *KeyringNames) EncryptName(id model.ObjectID, name string) (string, error) {
	var encrypted string
	err := n.keyring.Use(func(k *Keys) error {
		var err error
		encrypted, err = NewNameCipher(k, n.random).EncryptName(id, name)
		return err
	})
	return encrypted, err
}

func (n *KeyringNames) DecryptName(id model.ObjectID, encoded string) (string, error) {
	var name string
	err := n.keyring.Use(func(k *Keys) error {
		var err error
		name, err = NewNameCipher(k, n.random).DecryptName(id, encoded)
		return err
	})
	return name, err
}
