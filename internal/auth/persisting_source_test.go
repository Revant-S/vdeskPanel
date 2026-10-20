package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/oauth2"
)

// scriptedSource hands out tokens in order, repeating the last one.
type scriptedSource struct {
	tokens []*oauth2.Token
	calls  int
}

func (s *scriptedSource) Token() (*oauth2.Token, error) {
	token := s.tokens[min(s.calls, len(s.tokens)-1)]
	s.calls++
	return token, nil
}

func newSavedStore(t *testing.T, initial *oauth2.Token) (*TokenStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.enc")
	store := NewTokenStore(path, newTestKeyring(t))
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func drawToken(t *testing.T, source oauth2.TokenSource) {
	t.Helper()
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
}

func loadSaved(t *testing.T, store *TokenStore) *oauth2.Token {
	t.Helper()
	token, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestUnchangedTokenIsNotRewritten(t *testing.T) {
	initial := newTestToken("access-1", "refresh-1")
	store, path := newSavedStore(t, initial)
	before, _ := os.ReadFile(path)
	drawToken(t, store.PersistingSource(&scriptedSource{tokens: []*oauth2.Token{initial}}))
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("token file was rewritten although nothing rotated")
	}
}

func TestRotatedAccessTokenIsSavedKeepingRefreshToken(t *testing.T) {
	store, _ := newSavedStore(t, newTestToken("access-1", "refresh-1"))
	refreshed := newTestToken("access-2", "")
	drawToken(t, store.PersistingSource(&scriptedSource{tokens: []*oauth2.Token{refreshed}}))
	assertSameToken(t, loadSaved(t, store), newTestToken("access-2", "refresh-1"))
}

func TestRotatedRefreshTokenIsSaved(t *testing.T) {
	store, _ := newSavedStore(t, newTestToken("access-1", "refresh-1"))
	source := store.PersistingSource(&scriptedSource{tokens: []*oauth2.Token{
		newTestToken("access-1", "refresh-1"),
		newTestToken("access-2", "refresh-2"),
	}})
	drawToken(t, source)
	drawToken(t, source)
	assertSameToken(t, loadSaved(t, store), newTestToken("access-2", "refresh-2"))
}

func TestFirstTokenIsSavedWhenNoneOnDisk(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.enc"), newTestKeyring(t))
	drawToken(t, store.PersistingSource(&scriptedSource{tokens: []*oauth2.Token{newTestToken("access-1", "refresh-1")}}))
	assertSameToken(t, loadSaved(t, store), newTestToken("access-1", "refresh-1"))
}

func TestFailedSaveIsReported(t *testing.T) {
	keyring := newTestKeyring(t)
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.enc"), keyring)
	keyring.Destroy()
	_, err := store.PersistingSource(&scriptedSource{tokens: []*oauth2.Token{newTestToken("a", "r")}}).Token()
	if err == nil {
		t.Fatal("Token succeeded although the rotated token could not be saved")
	}
}

type failingSource struct{}

var errRefreshFailed = errors.New("refresh failed")

func (failingSource) Token() (*oauth2.Token, error) { return nil, errRefreshFailed }

func TestBaseErrorPassesThrough(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "token.enc"), newTestKeyring(t))
	if _, err := store.PersistingSource(failingSource{}).Token(); !errors.Is(err, errRefreshFailed) {
		t.Fatalf("err = %v, want errRefreshFailed", err)
	}
}

func TestConcurrentTokenCallsSaveOnce(t *testing.T) {
	store, path := newSavedStore(t, newTestToken("access-1", "refresh-1"))
	source := store.PersistingSource(oauth2.StaticTokenSource(newTestToken("access-2", "refresh-1")))
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := source.Token(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	saved, _ := os.ReadFile(path)
	drawToken(t, source)
	if after, _ := os.ReadFile(path); !bytes.Equal(saved, after) {
		t.Fatal("token was saved again after it had already been persisted")
	}
	assertSameToken(t, loadSaved(t, store), newTestToken("access-2", "refresh-1"))
}
