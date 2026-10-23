package auth

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"vault/internal/crypto"
	"vault/internal/plaintextcheck"
)

var testKDFParams = crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}

func newTestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	_, keyring, err := crypto.CreateVault([]byte("correct horse battery"), testKDFParams, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keyring.Destroy)
	return keyring
}

func newTestToken(access, refresh string) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		Expiry:       time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
	}
}

func assertSameToken(t *testing.T, got, want *oauth2.Token) {
	t.Helper()
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken ||
		got.TokenType != want.TokenType || !got.Expiry.Equal(want.Expiry) {
		t.Fatalf("token = %+v, want %+v", got, want)
	}
}

func TestTokenStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "token.enc")
	store := NewTokenStore(path, newTestKeyring(t))
	want := newTestToken("access-secret-value", "refresh-secret-value")
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	assertSameToken(t, got, want)
}

func TestLoadWithoutSavedTokenIsErrNoToken(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.enc"), newTestKeyring(t))
	if _, err := store.Load(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestTokenFromAnotherVaultFailsToOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.enc")
	if err := NewTokenStore(path, newTestKeyring(t)).Save(newTestToken("a", "r")); err != nil {
		t.Fatal(err)
	}
	_, err := NewTokenStore(path, newTestKeyring(t)).Load()
	if !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("err = %v, want crypto.ErrDecrypt", err)
	}
}

func TestLockedKeyringRefusesTokenAccess(t *testing.T) {
	keyring := newTestKeyring(t)
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.enc"), keyring)
	keyring.Destroy()
	if err := store.Save(newTestToken("a", "r")); !errors.Is(err, crypto.ErrLocked) {
		t.Fatalf("Save err = %v, want ErrLocked", err)
	}
}

func TestTokenFileIsPrivateAndHoldsNoPlaintext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(home, "token.enc")
	store := NewTokenStore(path, newTestKeyring(t))
	for range 2 {
		if err := store.Save(newTestToken("access-secret-value", "refresh-secret-value")); err != nil {
			t.Fatal(err)
		}
	}
	assertMode(t, path, 0o600)
	assertMode(t, home, 0o700|os.ModeDir)
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("home holds %v (%v), want only token.enc", entries, err)
	}
	plaintextcheck.AssertDirClean(t, home, "access-secret-value", "refresh-secret-value")
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Fatalf("%s mode = %v, want %v", filepath.Base(path), info.Mode(), want)
	}
}
