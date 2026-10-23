package crypto

import (
	"fmt"

	"github.com/cosmoboy/vault/internal/secret"
	"golang.org/x/crypto/argon2"
)

type KDFParams struct {
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
}

func DefaultKDFParams() KDFParams {
	return KDFParams{Time: 3, MemoryKiB: 256 * 1024, Threads: 4}
}

// The upper bounds stop a hostile header from exhausting memory during unlock. A weak header
// cannot be forged without the password, so the lower bounds only reject nonsense.
func (p *KDFParams) Validate() error {
	switch {
	case p.Time < 1 || p.Time > 10:
		return fmt.Errorf("kdf time %d outside 1..10", p.Time)
	case p.MemoryKiB < 8*1024 || p.MemoryKiB > 4*1024*1024:
		return fmt.Errorf("kdf memory %d KiB outside 8 MiB..4 GiB", p.MemoryKiB)
	case p.Threads < 1 || p.Threads > 16:
		return fmt.Errorf("kdf threads %d outside 1..16", p.Threads)
	}
	return nil
}

func deriveKeyEncryptionKey(password []byte, salt []byte, params KDFParams) (*secret.Key, error) {
	material := argon2.IDKey(password, salt, params.Time, params.MemoryKiB, params.Threads, KeySize)
	return secret.NewKey(material)
}
