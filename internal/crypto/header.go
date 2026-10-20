package crypto

import (
	"encoding/json"
	"errors"
	"fmt"
)

const CurrentFormatVersion = 1

var ErrUnsupportedFormat = errors.New("crypto: vault format is newer than this build")

const (
	saltSize             = 32
	vaultIDSize          = 16
	wrappedMasterKeySize = Overhead + KeySize
)

// VaultHeader is stored as Vault/vault.header on Drive. It holds no secret in the clear.
type VaultHeader struct {
	FormatVersion    int
	VaultID          [vaultIDSize]byte
	KDF              KDFParams
	Salt             [saltSize]byte
	WrappedMasterKey []byte
}

type headerJSON struct {
	FormatVersion    int       `json:"formatVersion"`
	VaultID          []byte    `json:"vaultId"`
	KDF              KDFParams `json:"kdf"`
	Salt             []byte    `json:"salt"`
	WrappedMasterKey []byte    `json:"wrappedMasterKey"`
}

func MarshalVaultHeader(h VaultHeader) ([]byte, error) {
	return json.MarshalIndent(headerJSON{
		FormatVersion:    h.FormatVersion,
		VaultID:          h.VaultID[:],
		KDF:              h.KDF,
		Salt:             h.Salt[:],
		WrappedMasterKey: h.WrappedMasterKey,
	}, "", "  ")
}

func ParseVaultHeader(data []byte) (VaultHeader, error) {
	var raw headerJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return VaultHeader{}, fmt.Errorf("parse vault header: %w", err)
	}
	if raw.FormatVersion > CurrentFormatVersion {
		return VaultHeader{}, fmt.Errorf("vault format %d: %w", raw.FormatVersion, ErrUnsupportedFormat)
	}
	if raw.FormatVersion < 1 {
		return VaultHeader{}, fmt.Errorf("vault format %d is invalid", raw.FormatVersion)
	}
	if err := raw.KDF.Validate(); err != nil {
		return VaultHeader{}, fmt.Errorf("vault header: %w", err)
	}
	if len(raw.VaultID) != vaultIDSize || len(raw.Salt) != saltSize || len(raw.WrappedMasterKey) != wrappedMasterKeySize {
		return VaultHeader{}, errors.New("vault header: field sizes are wrong")
	}
	h := VaultHeader{FormatVersion: raw.FormatVersion, KDF: raw.KDF, WrappedMasterKey: raw.WrappedMasterKey}
	copy(h.VaultID[:], raw.VaultID)
	copy(h.Salt[:], raw.Salt)
	return h, nil
}
