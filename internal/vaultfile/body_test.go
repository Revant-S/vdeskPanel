package vaultfile

import (
	"bytes"
	"crypto/rand"
	"errors"
	"github.com/cosmoboy/vault/internal/crypto"
	"io"
	mathrand "math/rand/v2"
	"testing"
)

var edgeSizes = []int{0, 1, 65535, 65536, 65537, 3 * 65536, 3*65536 + 17}

func TestRoundTripAtSegmentEdges(t *testing.T) {
	content := testContentKey(t)
	for _, size := range edgeSizes {
		body, storage := newTestBody(t, content)
		data := randomBytes(size)
		if _, err := body.WriteAt(data, 0); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if got := int64(len(storage.bytes())); got != CiphertextSize(int64(size)) {
			t.Fatalf("size %d: ciphertext %d bytes, want %d", size, got, CiphertextSize(int64(size)))
		}
		reopened, err := OpenBody(storage, content, int64(size), rand.Reader)
		if err != nil {
			t.Fatalf("size %d: reopen: %v", size, err)
		}
		if !bytes.Equal(readAll(t, reopened), data) {
			t.Fatalf("size %d: content differs after reopen", size)
		}
		reopened.Close()
	}
}

func TestReadAtPastEndAndPartial(t *testing.T) {
	body, _ := newTestBody(t, testContentKey(t))
	data := randomBytes(70000)
	body.WriteAt(data, 0)
	buf := make([]byte, 100)
	if n, err := body.ReadAt(buf, 69950); n != 50 || err != io.EOF || !bytes.Equal(buf[:50], data[69950:]) {
		t.Fatalf("partial read = %d, %v", n, err)
	}
	if n, err := body.ReadAt(buf, 70000); n != 0 || err != io.EOF {
		t.Fatalf("read at end = %d, %v", n, err)
	}
}

func TestRandomOperationsMatchPlainModel(t *testing.T) {
	content := testContentKey(t)
	random := mathrand.New(mathrand.NewPCG(1, 2))
	for round := 0; round < 20; round++ {
		body, storage := newTestBody(t, content)
		var model []byte
		for step := 0; step < 30; step++ {
			model = applyRandomOperation(t, random, body, model)
			if int64(len(storage.bytes())) != CiphertextSize(int64(len(model))) {
				t.Fatalf("round %d step %d: storage size mismatch", round, step)
			}
		}
		if !bytes.Equal(readAll(t, body), model) {
			t.Fatalf("round %d: content differs from model", round)
		}
	}
}

func FuzzBodyMatchesPlainModel(f *testing.F) {
	f.Add(uint64(1), uint64(2))
	f.Add(uint64(65536), uint64(7))
	content := testContentKey(f)
	f.Fuzz(func(t *testing.T, seedA, seedB uint64) {
		random := mathrand.New(mathrand.NewPCG(seedA, seedB))
		body, _ := newTestBody(t, content)
		var model []byte
		for step := 0; step < 8; step++ {
			model = applyRandomOperation(t, random, body, model)
		}
		if !bytes.Equal(readAll(t, body), model) {
			t.Fatal("content differs from model")
		}
	})
}

func applyRandomOperation(t testing.TB, random *mathrand.Rand, body *Body, model []byte) []byte {
	t.Helper()
	interesting := []int{0, 1, 65535, 65536, 65537, 131072, 200000}
	pick := func() int {
		if random.IntN(2) == 0 {
			return interesting[random.IntN(len(interesting))]
		}
		return random.IntN(250000)
	}
	if random.IntN(4) == 0 {
		size := pick()
		if err := body.Truncate(int64(size)); err != nil {
			t.Fatalf("Truncate(%d): %v", size, err)
		}
		if size <= len(model) {
			return model[:size]
		}
		return append(model, make([]byte, size-len(model))...)
	}
	offset, length := pick(), random.IntN(140000)
	data := randomBytes(length)
	if _, err := body.WriteAt(data, int64(offset)); err != nil {
		t.Fatalf("WriteAt(%d, %d): %v", offset, length, err)
	}
	if end := offset + length; end > len(model) && length > 0 {
		model = append(model, make([]byte, end-len(model))...)
	}
	copy(model[offset:], data)
	return model
}

func TestRewriteUsesFreshNonce(t *testing.T) {
	body, storage := newTestBody(t, testContentKey(t))
	body.WriteAt([]byte("same"), 0)
	first := storage.bytes()[HeaderSize : HeaderSize+crypto.NonceSize]
	body.WriteAt([]byte("same"), 0)
	second := storage.bytes()[HeaderSize : HeaderSize+crypto.NonceSize]
	if bytes.Equal(first, second) {
		t.Fatal("segment rewrite reused its nonce")
	}
}

func TestTamperingFails(t *testing.T) {
	content := testContentKey(t)
	size := int64(3*SegmentPlaintextSize + 100)
	open := func(data []byte, plaintextSize int64) error {
		body, err := OpenBody(&memoryStorage{data: data}, content, plaintextSize, rand.Reader)
		if err != nil {
			return err
		}
		defer body.Close()
		_, err = body.ReadAt(make([]byte, plaintextSize), 0)
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	body, storage := newTestBody(t, content)
	body.WriteAt(randomBytes(int(size)), 0)
	original := storage.bytes()
	if err := open(original, size); err != nil {
		t.Fatalf("untampered body: %v", err)
	}

	flipped := append([]byte(nil), original...)
	flipped[SegmentOffset(1)+30] ^= 1
	swapped := append([]byte(nil), original...)
	copy(swapped[SegmentOffset(0):SegmentOffset(1)], original[SegmentOffset(1):SegmentOffset(2)])
	copy(swapped[SegmentOffset(1):SegmentOffset(2)], original[SegmentOffset(0):SegmentOffset(1)])
	truncated := original[:SegmentOffset(3)]
	appended := append(append([]byte(nil), original...), original[SegmentOffset(1):SegmentOffset(2)]...)
	headerSwapped := append([]byte(nil), original...)
	headerSwapped[6] ^= 1

	other, otherStorage := newTestBody(t, content)
	other.WriteAt(randomBytes(int(size)), 0)
	foreign := append([]byte(nil), original...)
	copy(foreign[SegmentOffset(1):SegmentOffset(2)], otherStorage.bytes()[SegmentOffset(1):SegmentOffset(2)])

	cases := []struct {
		name          string
		data          []byte
		plaintextSize int64
	}{
		{"bit flip", flipped, size},
		{"swapped segments", swapped, size},
		{"dropped final segment", truncated, 3 * SegmentPlaintextSize},
		{"appended segment", appended, size + SegmentPlaintextSize},
		{"segment from another body", foreign, size},
		{"header body id changed", headerSwapped, size},
	}
	for _, c := range cases {
		if err := open(c.data, c.plaintextSize); !errors.Is(err, ErrTampered) {
			t.Errorf("%s: error = %v, want ErrTampered", c.name, err)
		}
	}
}

func TestWrongContentKeyFails(t *testing.T) {
	body, storage := newTestBody(t, testContentKey(t))
	body.WriteAt([]byte("hello"), 0)
	if _, err := OpenBody(storage, testContentKey(t), 5, rand.Reader); !errors.Is(err, ErrTampered) {
		t.Fatalf("error = %v, want ErrTampered", err)
	}
}

func TestClosedBodyRefusesUse(t *testing.T) {
	body, _ := newTestBody(t, testContentKey(t))
	body.Close()
	if _, err := body.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadAt after Close = %v", err)
	}
	if _, err := body.WriteAt([]byte("x"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteAt after Close = %v", err)
	}
}

func TestLayoutSizesAreInverse(t *testing.T) {
	for _, size := range append(edgeSizes, 10*SegmentPlaintextSize, 5_000_000) {
		got, err := PlaintextSize(CiphertextSize(int64(size)))
		if err != nil || got != int64(size) {
			t.Fatalf("PlaintextSize(CiphertextSize(%d)) = %d, %v", size, got, err)
		}
	}
	for _, bad := range []int64{0, HeaderSize, HeaderSize + SegmentOverhead - 1, SegmentOffset(1) + 10} {
		if _, err := PlaintextSize(bad); !errors.Is(err, ErrCorruptLayout) {
			t.Errorf("PlaintextSize(%d) error = %v", bad, err)
		}
	}
	if SegmentOffset(2) != HeaderSize+2*(65536+40) {
		t.Fatal("segment offset formula changed")
	}
}

func randomBytes(size int) []byte {
	out := make([]byte, size)
	rand.Read(out)
	return out
}
