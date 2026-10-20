package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeTokenEndpoint plays Google's token endpoint and records what the client sent.
type fakeTokenEndpoint struct {
	mu            sync.Mutex
	verifier      string
	redirectURI   string
	grantedScopes string
	refreshToken  string
}

func (e *fakeTokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "good-code" {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	e.verifier = r.Form.Get("code_verifier")
	e.redirectURI = r.Form.Get("redirect_uri")
	response := map[string]any{
		"access_token":  "access-1",
		"token_type":    "Bearer",
		"refresh_token": e.refreshToken,
		"expires_in":    3600,
		"scope":         e.grantedScopes,
	}
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (e *fakeTokenEndpoint) received() (verifier, redirectURI string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.verifier, e.redirectURI
}

func newTestFlow(t *testing.T) (*Flow, *fakeTokenEndpoint) {
	t.Helper()
	endpoint := &fakeTokenEndpoint{grantedScopes: DriveFileScope, refreshToken: "refresh-1"}
	server := httptest.NewServer(endpoint)
	t.Cleanup(server.Close)
	config := NewConfig(ClientCredentials{ClientID: "client", ClientSecret: "secret"})
	config.Endpoint = oauth2.Endpoint{
		AuthURL:   "https://accounts.example/auth",
		TokenURL:  server.URL,
		AuthStyle: oauth2.AuthStyleInParams,
	}
	return NewFlow(config, nil), endpoint
}

type browserVisit struct {
	status int
	body   string
	err    error
}

// visitCallback acts as the browser after Google redirects back with the given parameters.
func visitCallback(authURL string, edit func(url.Values)) <-chan browserVisit {
	visits := make(chan browserVisit, 1)
	go func() {
		parsed, _ := url.Parse(authURL)
		query := url.Values{"code": {"good-code"}, "state": {parsed.Query().Get("state")}}
		edit(query)
		response, err := http.Get(parsed.Query().Get("redirect_uri") + "?" + query.Encode())
		if err != nil {
			visits <- browserVisit{err: err}
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		visits <- browserVisit{status: response.StatusCode, body: string(body)}
	}()
	return visits
}

func beginSignIn(t *testing.T, flow *Flow) (string, func(context.Context) (*oauth2.Token, error)) {
	t.Helper()
	authURL, wait, err := flow.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return authURL, wait
}

func TestSignInSendsMatchingPKCEVerifier(t *testing.T) {
	flow, endpoint := newTestFlow(t)
	authURL, wait := beginSignIn(t, flow)
	visit := visitCallback(authURL, func(url.Values) {})
	token, err := wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token.RefreshToken != "refresh-1" || token.AccessToken != "access-1" {
		t.Fatalf("token = %+v", token)
	}
	query := mustQuery(t, authURL)
	verifier, redirectURI := endpoint.received()
	if verifier == "" || oauth2.S256ChallengeFromVerifier(verifier) != query.Get("code_challenge") {
		t.Fatalf("verifier %q does not match challenge %q", verifier, query.Get("code_challenge"))
	}
	if redirectURI != query.Get("redirect_uri") {
		t.Fatalf("token request redirect_uri = %q, auth URL had %q", redirectURI, query.Get("redirect_uri"))
	}
	result := <-visit
	if result.err != nil || result.status != http.StatusOK || !strings.Contains(result.body, "Signed in") {
		t.Fatalf("browser saw %+v", result)
	}
}

func TestAuthURLRequestsOfflineConsentAndDriveFileOnly(t *testing.T) {
	flow, _ := newTestFlow(t)
	authURL, _ := beginSignIn(t, flow)
	query := mustQuery(t, authURL)
	want := map[string]string{
		"access_type":           "offline",
		"prompt":                "consent",
		"scope":                 DriveFileScope,
		"code_challenge_method": "S256",
		"response_type":         "code",
	}
	for key, value := range want {
		if query.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, query.Get(key), value)
		}
	}
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "0" {
		t.Errorf("redirect_uri = %q", query.Get("redirect_uri"))
	}
	if len(query.Get("state")) < 20 {
		t.Errorf("state %q is too short", query.Get("state"))
	}
}

func TestStateMismatchIsRejected(t *testing.T) {
	flow, endpoint := newTestFlow(t)
	authURL, wait := beginSignIn(t, flow)
	visit := visitCallback(authURL, func(query url.Values) { query.Set("state", "forged") })
	_, err := wait(t.Context())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("err = %v, want ErrStateMismatch", err)
	}
	if verifier, _ := endpoint.received(); verifier != "" {
		t.Fatal("code was exchanged despite a state mismatch")
	}
	if result := <-visit; result.status != http.StatusBadRequest || strings.Contains(result.body, "Signed in") {
		t.Fatalf("browser saw %+v", result)
	}
}

func TestAccessDeniedSurfacesAnError(t *testing.T) {
	flow, _ := newTestFlow(t)
	authURL, wait := beginSignIn(t, flow)
	visit := visitCallback(authURL, func(query url.Values) {
		query.Del("code")
		query.Set("error", "access_denied")
	})
	_, err := wait(t.Context())
	if !errors.Is(err, ErrSignInDenied) || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want ErrSignInDenied with access_denied", err)
	}
	<-visit
}

func TestWaitTimesOutAndClosesListener(t *testing.T) {
	flow, _ := newTestFlow(t)
	authURL, wait := beginSignIn(t, flow)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	redirect, _ := url.Parse(mustQuery(t, authURL).Get("redirect_uri"))
	if connection, err := net.Dial("tcp", redirect.Host); err == nil {
		connection.Close()
		t.Fatal("callback listener still accepts connections after timeout")
	}
}

func TestCancellingBeginContextStopsWaiting(t *testing.T) {
	flow, _ := newTestFlow(t)
	ctx, cancel := context.WithCancel(t.Context())
	_, wait, err := flow.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := wait(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
}

func TestMissingRefreshTokenOrScopeFails(t *testing.T) {
	cases := []struct {
		name          string
		refreshToken  string
		grantedScopes string
		want          error
	}{
		{"no refresh token", "", DriveFileScope, ErrNoRefreshToken},
		{"drive unticked", "refresh-1", "openid email", ErrScopeNotGranted},
	}
	for _, tc := range cases {
		flow, endpoint := newTestFlow(t)
		endpoint.refreshToken, endpoint.grantedScopes = tc.refreshToken, tc.grantedScopes
		authURL, wait := beginSignIn(t, flow)
		visit := visitCallback(authURL, func(url.Values) {})
		if _, err := wait(t.Context()); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if result := <-visit; strings.Contains(result.body, "Signed in") {
			t.Errorf("%s: browser was told sign-in succeeded", tc.name)
		}
	}
}

func TestSecondCallbackAndSecondWaitAreRefused(t *testing.T) {
	flow, _ := newTestFlow(t)
	authURL, wait := beginSignIn(t, flow)
	first := visitCallback(authURL, func(url.Values) {})
	if _, err := wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-first
	if _, err := wait(t.Context()); !errors.Is(err, ErrAlreadyWaiting) {
		t.Fatalf("second wait err = %v", err)
	}
}

func TestNonLoopbackListenerIsRefused(t *testing.T) {
	config := NewConfig(ClientCredentials{ClientID: "client", ClientSecret: "secret"})
	listenAnywhere := func() (net.Listener, error) { return net.Listen("tcp", "0.0.0.0:0") }
	if _, _, err := NewFlow(config, listenAnywhere).Begin(t.Context()); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("err = %v, want ErrNotLoopback", err)
	}
}

func mustQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}
