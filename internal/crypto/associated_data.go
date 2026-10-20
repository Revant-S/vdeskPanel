package crypto

import (
	"encoding/binary"
)

// Every envelope binds a purpose label and the identity of what it protects, so a ciphertext
// cannot be moved to another slot, object or vault without failing authentication.

func HeaderAD(vaultID [16]byte) []byte {
	return concat([]byte("vault/v1/header"), vaultID[:])
}

func NameAD(vaultID [16]byte, objectID [16]byte) []byte {
	return concat([]byte("vault/v1/name"), vaultID[:], objectID[:])
}

func FileKeyAD(formatVersion uint16, bodyID [16]byte) []byte {
	return concat([]byte("vault/v1/file-key"), binary.BigEndian.AppendUint16(nil, formatVersion), bodyID[:])
}

func SegmentAD(bodyID [16]byte, index uint64, final bool) []byte {
	ad := binary.BigEndian.AppendUint64(append([]byte(nil), bodyID[:]...), index)
	if final {
		return append(ad, 1)
	}
	return append(ad, 0)
}

func CommitAD(commitID [16]byte) []byte {
	return concat([]byte("vault/v1/commit"), commitID[:])
}

func PackAD(packFileName string) []byte {
	return concat([]byte("vault/v1/pack\x00"), []byte(packFileName))
}

func DeviceAD(deviceID string) []byte {
	return concat([]byte("vault/v1/device\x00"), []byte(deviceID))
}

func TokenAD(vaultID [16]byte) []byte {
	return concat([]byte("vault/v1/token"), vaultID[:])
}

func IndexAD(table, column, primaryKey string) []byte {
	return concat([]byte("vault/v1/index/"+table+"."+column+"\x00"), []byte(primaryKey))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}
