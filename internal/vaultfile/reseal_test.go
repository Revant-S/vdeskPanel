package vaultfile

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestResealCopiesContentUnderNewIdentity(t *testing.T) {
	content := testContentKey(t)
	for _, size := range edgeSizes {
		src, _ := newTestBody(t, content)
		data := randomBytes(size)
		src.WriteAt(data, 0)
		dst := &memoryStorage{}
		result, err := Reseal(src, dst, content, rand.Reader)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if result.Header.BodyID == src.Header().BodyID {
			t.Fatalf("size %d: reseal kept the body id", size)
		}
		if result.PlaintextSHA256 != sha256.Sum256(data) {
			t.Fatalf("size %d: plaintext hash wrong", size)
		}
		if result.CiphertextMD5 != md5.Sum(dst.bytes()) {
			t.Fatalf("size %d: ciphertext md5 does not match written bytes", size)
		}
		copyBody, err := OpenBody(dst, content, int64(size), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(readAll(t, copyBody), data) {
			t.Fatalf("size %d: resealed content differs", size)
		}
		copyBody.Close()
		header, sum, err := VerifyBody(dst, int64(len(dst.bytes())), content)
		if err != nil || sum != result.PlaintextSHA256 || header.BodyID != result.Header.BodyID {
			t.Fatalf("size %d: VerifyBody = %v", size, err)
		}
	}
}

func TestResealRefusesCorruptSourceSegment(t *testing.T) {
	content := testContentKey(t)
	src, storage := newTestBody(t, content)
	src.WriteAt(randomBytes(3*SegmentPlaintextSize), 0)
	storage.data[SegmentOffset(2)+50] ^= 1
	_, err := Reseal(src, &memoryStorage{}, content, rand.Reader)
	var segmentErr *SegmentError
	if !errors.As(err, &segmentErr) || segmentErr.Index != 2 || !errors.Is(err, ErrTampered) {
		t.Fatalf("error = %v, want SegmentError at 2", err)
	}
}

func TestReplayingSegmentFromOlderVersionFails(t *testing.T) {
	content := testContentKey(t)
	live, _ := newTestBody(t, content)
	live.WriteAt(randomBytes(2*SegmentPlaintextSize), 0)
	older := &memoryStorage{}
	if _, err := Reseal(live, older, content, rand.Reader); err != nil {
		t.Fatal(err)
	}
	live.WriteAt(randomBytes(100), 0)
	newer := &memoryStorage{}
	if _, err := Reseal(live, newer, content, rand.Reader); err != nil {
		t.Fatal(err)
	}
	spliced := newer.bytes()
	copy(spliced[SegmentOffset(0):SegmentOffset(1)], older.bytes()[SegmentOffset(0):SegmentOffset(1)])
	if _, _, err := VerifyBody(&memoryStorage{data: spliced}, int64(len(spliced)), content); !errors.Is(err, ErrTampered) {
		t.Fatalf("spliced older segment: error = %v, want ErrTampered", err)
	}
}

func TestVerifyBodyRejectsBadLayout(t *testing.T) {
	if _, _, err := VerifyBody(&memoryStorage{data: make([]byte, 10)}, 10, testContentKey(t)); err == nil {
		t.Fatal("VerifyBody accepted a 10-byte body")
	}
}
