package crypto

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"vault/internal/model"
)

var testParams = KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}

func newTestVault(t *testing.T, password string) (VaultHeader, *Keyring) {
	t.Helper()
	header, keyring, err := CreateVault([]byte(password), testParams, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keyring.Destroy)
	return header, keyring
}

func useKeys(t *testing.T, keyring *Keyring) *Keys {
	t.Helper()
	var keys *Keys
	if err := keyring.Use(func(k *Keys) error { keys = k; return nil }); err != nil {
		t.Fatal(err)
	}
	return keys
}

func testObjectID(t *testing.T) model.ObjectID {
	t.Helper()
	id, err := model.NewObjectID(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUnlockWithCorrectPasswordRecoversSameKeys(t *testing.T) {
	header, created := newTestVault(t, "correct horse battery")
	id := testObjectID(t)
	encrypted, err := NewNameCipher(useKeys(t, created), rand.Reader).EncryptName(id, "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	unlocked, err := UnlockVault(header, []byte("correct horse battery"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlocked.Destroy()
	name, err := NewNameCipher(useKeys(t, unlocked), rand.Reader).DecryptName(id, encrypted)
	if err != nil || name != "report.pdf" {
		t.Fatalf("DecryptName = %q, %v", name, err)
	}
	createdMAC := ContentMAC(useKeys(t, created), sha256.Sum256([]byte("x")))
	unlockedMAC := ContentMAC(useKeys(t, unlocked), sha256.Sum256([]byte("x")))
	if !bytes.Equal(createdMAC, unlockedMAC) {
		t.Fatal("content MAC differs between create and unlock")
	}
}

func TestUnlockWithWrongPasswordFails(t *testing.T) {
	header, _ := newTestVault(t, "correct horse battery")
	if _, err := UnlockVault(header, []byte("wrong horse battery")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("error = %v, want ErrWrongPassword", err)
	}
}

func TestUnlockFailsWhenVaultIDIsSwapped(t *testing.T) {
	header, _ := newTestVault(t, "correct horse battery")
	header.VaultID[0] ^= 1
	if _, err := UnlockVault(header, []byte("correct horse battery")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("error = %v, want ErrWrongPassword", err)
	}
}

func TestPasswordIsWiped(t *testing.T) {
	password := []byte("correct horse battery")
	header, keyring, err := CreateVault(password, testParams, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring.Destroy()
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("CreateVault left the password in memory")
	}
	password = []byte("correct horse battery")
	if _, err := UnlockVault(header, password); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("UnlockVault left the password in memory")
	}
}

func TestHeaderRoundTripAndValidation(t *testing.T) {
	header, _ := newTestVault(t, "correct horse battery")
	data, err := MarshalVaultHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseVaultHeader(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.VaultID != header.VaultID || parsed.Salt != header.Salt || !bytes.Equal(parsed.WrappedMasterKey, header.WrappedMasterKey) || parsed.KDF != header.KDF {
		t.Fatal("header changed in round trip")
	}

	cases := map[string]func(*VaultHeader){
		"newer format":   func(h *VaultHeader) { h.FormatVersion = CurrentFormatVersion + 1 },
		"huge memory":    func(h *VaultHeader) { h.KDF.MemoryKiB = 1 << 30 },
		"zero threads":   func(h *VaultHeader) { h.KDF.Threads = 0 },
		"short key wrap": func(h *VaultHeader) { h.WrappedMasterKey = h.WrappedMasterKey[:10] },
	}
	for name, mutate := range cases {
		modified := header
		mutate(&modified)
		data, _ := MarshalVaultHeader(modified)
		if _, err := ParseVaultHeader(data); err == nil {
			t.Errorf("%s: ParseVaultHeader accepted it", name)
		}
	}
	newer := header
	newer.FormatVersion = CurrentFormatVersion + 1
	data, _ = MarshalVaultHeader(newer)
	if _, err := ParseVaultHeader(data); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("newer format error = %v", err)
	}
}

func TestHeaderJSONHoldsNoSecret(t *testing.T) {
	header, keyring := newTestVault(t, "correct horse battery")
	data, _ := MarshalVaultHeader(header)
	if bytes.Contains(data, []byte("correct horse")) {
		t.Fatal("header contains the password")
	}
	keys := useKeys(t, keyring)
	if bytes.Contains(data, keys.IndexMAC.Bytes()) {
		t.Fatal("header contains a subkey")
	}
}

func TestNameCipherBindsObjectID(t *testing.T) {
	_, keyring := newTestVault(t, "correct horse battery")
	names := NewNameCipher(useKeys(t, keyring), rand.Reader)
	first, second := testObjectID(t), testObjectID(t)
	encrypted, err := names.EncryptName(first, "gate.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(encrypted, "+/=") {
		t.Fatalf("name %q is not unpadded base64url", encrypted)
	}
	if _, err := names.DecryptName(second, encrypted); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("decrypt under another oid = %v, want ErrDecrypt", err)
	}
	again, _ := names.EncryptName(first, "gate.md")
	if again == encrypted {
		t.Fatal("name encryption is deterministic")
	}
}

func TestNameValidation(t *testing.T) {
	_, keyring := newTestVault(t, "correct horse battery")
	names := NewNameCipher(useKeys(t, keyring), rand.Reader)
	id := testObjectID(t)
	if _, err := names.EncryptName(id, strings.Repeat("a", 256)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("256-byte name error = %v", err)
	}
	if _, err := names.EncryptName(id, strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255-byte name error = %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00b"} {
		if _, err := names.EncryptName(id, bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("EncryptName(%q) error = %v", bad, err)
		}
	}
	if _, err := names.EncryptName(model.RootID, "x"); err == nil {
		t.Fatal("root accepted as an object id for a name")
	}
}

func TestSubkeysAreSeparated(t *testing.T) {
	_, keyring := newTestVault(t, "correct horse battery")
	keys := useKeys(t, keyring)
	sealed, err := Seal(keys.Name, rand.Reader, []byte("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []struct {
		name string
		aead interface {
			Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		}
	}{{"content", keys.Content}, {"log", keys.Log}, {"token", keys.Token}, {"index", keys.IndexSeal}} {
		if _, err := other.aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], nil); err == nil {
			t.Errorf("%s key opened a name-key message", other.name)
		}
	}
}

func TestSealRejectsTamperingAndWrongAD(t *testing.T) {
	_, keyring := newTestVault(t, "correct horse battery")
	keys := useKeys(t, keyring)
	sealed, _ := Seal(keys.Log, rand.Reader, []byte("commit body"), []byte("ad"))
	if plain, err := Open(keys.Log, sealed, []byte("ad")); err != nil || string(plain) != "commit body" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	if _, err := Open(keys.Log, sealed, []byte("other")); !errors.Is(err, ErrDecrypt) {
		t.Fatal("wrong associated data accepted")
	}
	for i := range sealed {
		tampered := append([]byte(nil), sealed...)
		tampered[i] ^= 0x80
		if _, err := Open(keys.Log, tampered, []byte("ad")); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flip at byte %d accepted", i)
		}
	}
	if _, err := Open(keys.Log, sealed[:Overhead-1], nil); !errors.Is(err, ErrDecrypt) {
		t.Fatal("short message accepted")
	}
}

func TestAssociatedDataIsUnambiguous(t *testing.T) {
	if bytes.Equal(IndexAD("objects", "enc_path", "x1"), IndexAD("objects", "enc_pat", "hx1")) {
		t.Fatal("index AD collides across column boundaries")
	}
	var body [16]byte
	if bytes.Equal(SegmentAD(body, 1, true), SegmentAD(body, 1, false)) {
		t.Fatal("final flag not in segment AD")
	}
	if len(SegmentAD(body, 0, false)) != 16+8+1 {
		t.Fatal("segment AD layout changed")
	}
}

func TestKeyringDestroyWaitsForInFlightUse(t *testing.T) {
	_, keyring := newTestVault(t, "correct horse battery")
	inUse := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = keyring.Use(func(k *Keys) error {
			close(inUse)
			<-release
			if !k.IndexMAC.Alive() {
				t.Error("key destroyed during use")
			}
			return nil
		})
	}()
	<-inUse
	destroyed := make(chan struct{})
	go func() { keyring.Destroy(); close(destroyed) }()
	select {
	case <-destroyed:
		t.Fatal("Destroy did not wait for the in-flight use")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-destroyed
	wg.Wait()
	if err := keyring.Use(func(*Keys) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Fatalf("Use after Destroy = %v, want ErrLocked", err)
	}
}

func TestDefaultKDFParams(t *testing.T) {
	if testing.Short() {
		t.Skip("default Argon2id parameters allocate 256 MiB")
	}
	header, keyring, err := CreateVault([]byte("correct horse battery"), DefaultKDFParams(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring.Destroy()
	if header.KDF != (KDFParams{Time: 3, MemoryKiB: 256 * 1024, Threads: 4}) {
		t.Fatalf("default params = %+v", header.KDF)
	}
	unlocked, err := UnlockVault(header, []byte("correct horse battery"))
	if err != nil {
		t.Fatal(err)
	}
	unlocked.Destroy()
}
