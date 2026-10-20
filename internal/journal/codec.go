package journal

import (
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/klauspost/compress/zstd"
)

const maxDecodedSize = 64 << 20

type Commit struct {
	ID         string         `json:"id"`
	Device     model.DeviceID `json:"device"`
	DeviceName string         `json:"deviceName"`
	Clock      model.Stamp    `json:"clock"`
	Parents    []string       `json:"parents"`
	Ops        []Op           `json:"ops"`
}

// Codec turns commits into encrypted, compressed blobs. The zstd coders are safe for
// concurrent EncodeAll and DecodeAll calls.
type Codec struct {
	encoder *zstd.Encoder
	decoder *zstd.Decoder
	random  io.Reader
}

func NewCodec(random io.Reader) (*Codec, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedSize), zstd.WithDecoderConcurrency(0))
	if err != nil {
		return nil, fmt.Errorf("create zstd decoder: %w", err)
	}
	return &Codec{encoder: encoder, decoder: decoder, random: random}, nil
}

func (c *Codec) Close() {
	c.decoder.Close()
}

func (c *Codec) SealCommit(commit Commit, logKey cipher.AEAD) ([]byte, error) {
	id, err := commitIDBytes(commit.ID)
	if err != nil {
		return nil, err
	}
	return c.seal(commit, logKey, crypto.CommitAD(id))
}

func (c *Codec) OpenCommit(commitID string, sealed []byte, logKey cipher.AEAD) (Commit, error) {
	id, err := commitIDBytes(commitID)
	if err != nil {
		return Commit{}, err
	}
	var commit Commit
	if err := c.open(sealed, logKey, crypto.CommitAD(id), &commit); err != nil {
		return Commit{}, fmt.Errorf("open commit %s: %w", commitID, err)
	}
	if commit.ID != commitID {
		return Commit{}, fmt.Errorf("commit %s claims id %s", commitID, commit.ID)
	}
	return commit, nil
}

func (c *Codec) SealPack(fileName string, commits []Commit, logKey cipher.AEAD) ([]byte, error) {
	return c.seal(commits, logKey, crypto.PackAD(fileName))
}

func (c *Codec) OpenPack(fileName string, sealed []byte, logKey cipher.AEAD) ([]Commit, error) {
	var commits []Commit
	if err := c.open(sealed, logKey, crypto.PackAD(fileName), &commits); err != nil {
		return nil, fmt.Errorf("open pack %s: %w", fileName, err)
	}
	return commits, nil
}

func (c *Codec) seal(value any, key cipher.AEAD, additionalData []byte) ([]byte, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode log entry: %w", err)
	}
	return crypto.Seal(key, c.random, c.encoder.EncodeAll(plaintext, nil), additionalData)
}

func (c *Codec) open(sealed []byte, key cipher.AEAD, additionalData []byte, into any) error {
	compressed, err := crypto.Open(key, sealed, additionalData)
	if err != nil {
		return err
	}
	plaintext, err := c.decoder.DecodeAll(compressed, nil)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	return json.Unmarshal(plaintext, into)
}

func NewCommitID(random io.Reader) (string, error) {
	return model.NewRandomID(random)
}

func commitIDBytes(commitID string) ([16]byte, error) {
	var id [16]byte
	decoded, err := hex.DecodeString(commitID)
	if err != nil || len(decoded) != len(id) {
		return id, fmt.Errorf("commit id %q is not 16 hex bytes", commitID)
	}
	copy(id[:], decoded)
	return id, nil
}
