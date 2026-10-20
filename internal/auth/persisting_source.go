package auth

import (
	"fmt"
	"sync"

	"golang.org/x/oauth2"
)

// PersistingSource saves every token base hands out that differs from the saved one.
// A failed save is returned as an error so the next call retries it; silently keeping a
// rotated refresh token only in memory would lose it on restart.
func (s *TokenStore) PersistingSource(base oauth2.TokenSource) oauth2.TokenSource {
	return &persistingSource{store: s, base: base}
}

type persistingSource struct {
	store *TokenStore
	base  oauth2.TokenSource
	mu    sync.Mutex
	saved *oauth2.Token
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	token, err := p.base.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.saved == nil {
		p.saved, _ = p.store.Load()
	}
	if p.saved != nil && !hasRotated(p.saved, token) {
		return token, nil
	}
	toSave := keepRefreshToken(token, p.saved)
	if err := p.store.Save(toSave); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	p.saved = toSave
	return token, nil
}

func hasRotated(saved, current *oauth2.Token) bool {
	return current.AccessToken != saved.AccessToken ||
		(current.RefreshToken != "" && current.RefreshToken != saved.RefreshToken)
}

// keepRefreshToken covers sources that, like Google's refresh response, omit the refresh
// token when it has not changed.
func keepRefreshToken(token, saved *oauth2.Token) *oauth2.Token {
	if token.RefreshToken != "" || saved == nil {
		return token
	}
	merged := *token
	merged.RefreshToken = saved.RefreshToken
	return &merged
}
