package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const refreshMargin = time.Minute

// WallClockSource refreshes access tokens by wall-clock time. The oauth2 package compares
// expiry using Go's monotonic clock, which stops while a laptop sleeps, so after a night in
// suspend it keeps handing out a token Google rejected hours ago.
type WallClockSource struct {
	ctx     context.Context
	config  *oauth2.Config
	mu      sync.Mutex
	current *oauth2.Token
	now     func() time.Time
}

func NewWallClockSource(ctx context.Context, config *oauth2.Config, token *oauth2.Token) *WallClockSource {
	return &WallClockSource{ctx: ctx, config: config, current: token, now: time.Now}
}

func (s *WallClockSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.AccessToken != "" && s.now().Round(0).Add(refreshMargin).Before(s.current.Expiry.Round(0)) {
		return s.current, nil
	}
	return s.refreshLocked()
}

// RefreshToken forces a new access token, for when Google answers 401 to the current one.
func (s *WallClockSource) RefreshToken(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.refreshLocked()
	return err
}

func (s *WallClockSource) refreshLocked() (*oauth2.Token, error) {
	stale := &oauth2.Token{RefreshToken: s.current.RefreshToken}
	fresh, err := s.config.TokenSource(s.ctx, stale).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh Google access token: %w", err)
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = s.current.RefreshToken
	}
	s.current = fresh
	return fresh, nil
}
