package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
)

// ContentMAC is a keyed fingerprint of plaintext, equal across devices so dedup can match it
// without revealing the plaintext hash to anyone holding the index.
func ContentMAC(keys *Keys, plaintextSHA256 [32]byte) []byte {
	mac := hmac.New(sha256.New, keys.IndexMAC.Bytes())
	mac.Write(plaintextSHA256[:])
	return mac.Sum(nil)
}
