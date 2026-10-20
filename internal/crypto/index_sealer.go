package crypto

import "io"

// IndexSealer encrypts index columns that would otherwise hold names or paths.
type IndexSealer struct {
	keyring *Keyring
	random  io.Reader
}

func NewIndexSealer(keyring *Keyring, random io.Reader) *IndexSealer {
	return &IndexSealer{keyring: keyring, random: random}
}

func (s *IndexSealer) Seal(additionalData, plaintext []byte) ([]byte, error) {
	var sealed []byte
	err := s.keyring.Use(func(k *Keys) error {
		var err error
		sealed, err = Seal(k.IndexSeal, s.random, plaintext, additionalData)
		return err
	})
	return sealed, err
}

func (s *IndexSealer) Open(additionalData, sealed []byte) ([]byte, error) {
	var plaintext []byte
	err := s.keyring.Use(func(k *Keys) error {
		var err error
		plaintext, err = Open(k.IndexSeal, sealed, additionalData)
		return err
	})
	return plaintext, err
}
