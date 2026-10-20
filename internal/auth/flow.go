package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
)

var (
	ErrStateMismatch   = errors.New("auth: sign-in callback state does not match")
	ErrSignInDenied    = errors.New("auth: sign-in was not granted")
	ErrNoRefreshToken  = errors.New("auth: Google returned no refresh token")
	ErrScopeNotGranted = errors.New("auth: Drive access was not granted")
	ErrNotLoopback     = errors.New("auth: callback listener is not on a loopback address")
	ErrAlreadyWaiting  = errors.New("auth: sign-in is already being waited for")
)

const callbackShutdownTimeout = 2 * time.Second

// Flow runs the desktop-app sign-in. It never opens a browser itself: the daemon may have
// no display, so the URL goes back to the client that asked (D27).
type Flow struct {
	config *oauth2.Config
	listen func() (net.Listener, error)
}

func ListenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// NewFlow uses ListenLoopback when listen is nil.
func NewFlow(config *oauth2.Config, listen func() (net.Listener, error)) *Flow {
	if listen == nil {
		listen = ListenLoopback
	}
	return &Flow{config: config, listen: listen}
}

// Begin starts the callback listener, which lives until wait returns or ctx ends.
// wait may be called once; it exchanges the code with the PKCE verifier.
func (f *Flow) Begin(ctx context.Context) (string, func(context.Context) (*oauth2.Token, error), error) {
	listener, err := f.listen()
	if err != nil {
		return "", nil, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	redirectURL, err := loopbackRedirectURL(listener.Addr())
	if err != nil {
		listener.Close()
		return "", nil, err
	}
	session := newSignIn(ctx, *f.config, redirectURL)
	session.serve(listener)
	authURL := session.config.AuthCodeURL(session.state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(session.verifier))
	return authURL, session.wait, nil
}

func loopbackRedirectURL(address net.Addr) (string, error) {
	tcp, ok := address.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return "", fmt.Errorf("%w: %s", ErrNotLoopback, address)
	}
	return "http://" + tcp.String() + "/", nil
}

type callback struct {
	code  string
	err   error
	reply chan<- error
}

type signIn struct {
	listenerContext context.Context
	config          oauth2.Config
	state           string
	verifier        string
	server          *http.Server
	callbacks       chan callback
	stopWatching    func() bool
	claimed         atomic.Bool
	waiting         atomic.Bool
}

func newSignIn(ctx context.Context, config oauth2.Config, redirectURL string) *signIn {
	config.RedirectURL = redirectURL
	session := &signIn{
		listenerContext: ctx,
		config:          config,
		state:           rand.Text(),
		verifier:        oauth2.GenerateVerifier(),
		callbacks:       make(chan callback, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", session.handleCallback)
	session.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return session
}

func (s *signIn) serve(listener net.Listener) {
	go func() { _ = s.server.Serve(listener) }()
	s.stopWatching = context.AfterFunc(s.listenerContext, func() { s.server.Close() })
}

func (s *signIn) close() {
	s.stopWatching()
	s.server.Close()
}

// shutdownGracefully lets the callback page finish sending before the listener closes.
func (s *signIn) shutdownGracefully() {
	s.stopWatching()
	ctx, cancel := context.WithTimeout(context.Background(), callbackShutdownTimeout)
	defer cancel()
	if s.server.Shutdown(ctx) != nil {
		s.server.Close()
	}
}

// handleCallback accepts only the first real callback, so a second request cannot replace
// the code. A wrong state fails the whole sign-in rather than being silently skipped.
func (s *signIn) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !query.Has("state") && !query.Has("code") && !query.Has("error") {
		http.Error(w, "Not an OAuth callback.", http.StatusBadRequest)
		return
	}
	if !s.claimed.CompareAndSwap(false, true) {
		http.Error(w, "Sign-in was already handled.", http.StatusConflict)
		return
	}
	code, err := parseCallback(query, s.state)
	reply := make(chan error, 1)
	s.callbacks <- callback{code: code, err: err, reply: reply}
	select {
	case err = <-reply:
		writeCallbackPage(w, err)
	case <-r.Context().Done():
	}
}

func parseCallback(query url.Values, wantState string) (string, error) {
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(wantState)) != 1 {
		return "", ErrStateMismatch
	}
	if reason := query.Get("error"); reason != "" {
		return "", fmt.Errorf("%w: %s", ErrSignInDenied, reason)
	}
	code := query.Get("code")
	if code == "" {
		return "", fmt.Errorf("%w: callback carried no code", ErrSignInDenied)
	}
	return code, nil
}

func writeCallbackPage(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintln(w, "Sign-in failed. Return to Vault and try again.")
		return
	}
	fmt.Fprintln(w, "Signed in, you can close this tab.")
}

func (s *signIn) wait(ctx context.Context) (*oauth2.Token, error) {
	if !s.waiting.CompareAndSwap(false, true) {
		return nil, ErrAlreadyWaiting
	}
	var received callback
	select {
	case received = <-s.callbacks:
	case <-ctx.Done():
		s.close()
		return nil, fmt.Errorf("wait for sign-in: %w", context.Cause(ctx))
	case <-s.listenerContext.Done():
		s.close()
		return nil, fmt.Errorf("sign-in listener stopped: %w", context.Cause(s.listenerContext))
	}
	token, err := s.exchange(ctx, received)
	received.reply <- err
	s.shutdownGracefully()
	return token, err
}

func (s *signIn) exchange(ctx context.Context, received callback) (*oauth2.Token, error) {
	if received.err != nil {
		return nil, received.err
	}
	token, err := s.config.Exchange(ctx, received.code, oauth2.VerifierOption(s.verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	if token.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}
	if err := checkGrantedScopes(token, s.config.Scopes); err != nil {
		return nil, err
	}
	return token, nil
}

// checkGrantedScopes catches Google's granular consent, where the user may untick Drive
// access yet still complete sign-in.
func checkGrantedScopes(token *oauth2.Token, wanted []string) error {
	granted, _ := token.Extra("scope").(string)
	if granted == "" {
		return nil
	}
	grantedList := strings.Fields(granted)
	for _, scope := range wanted {
		if !slices.Contains(grantedList, scope) {
			return fmt.Errorf("%w: missing %s", ErrScopeNotGranted, scope)
		}
	}
	return nil
}
