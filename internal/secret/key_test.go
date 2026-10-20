package secret

import (
	"bytes"
	"testing"
)

func TestNewKeyMovesAndWipesMaterial(t *testing.T) {
	material := []byte("0123456789abcdef0123456789abcdef")
	original := append([]byte(nil), material...)
	key, err := NewKey(material)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	if !bytes.Equal(key.Bytes(), original) {
		t.Fatal("key bytes differ from material")
	}
	if !bytes.Equal(material, make([]byte, len(material))) {
		t.Fatal("source material not wiped")
	}
}

func TestNewKeyRejectsEmptyAllocation(t *testing.T) {
	if _, err := NewKey(nil); err != ErrLockedMemory {
		t.Fatalf("NewKey(nil) error = %v, want ErrLockedMemory", err)
	}
	if _, err := RandomKey(0); err != ErrLockedMemory {
		t.Fatalf("RandomKey(0) error = %v, want ErrLockedMemory", err)
	}
}

func TestRandomKeyIsRandomAndDestroyable(t *testing.T) {
	first, err := RandomKey(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomKey(32)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("two random keys are equal")
	}
	first.Destroy()
	if first.Alive() {
		t.Fatal("key alive after Destroy")
	}
	second.Destroy()
	var nilKey *Key
	nilKey.Destroy()
	if nilKey.Alive() {
		t.Fatal("nil key reports alive")
	}
}
