package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestWallClockSourceRefreshesExpiredAndForcedTokens(t *testing.T) {
	var issued atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"access-%d","token_type":"Bearer","expires_in":3600}`, issued.Add(1))
	}))
	defer server.Close()
	config := &oauth2.Config{ClientID: "id", ClientSecret: "secret", Endpoint: oauth2.Endpoint{TokenURL: server.URL}}
	now := time.Now()
	source := NewWallClockSource(context.Background(), config, &oauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: now.Add(time.Hour)})
	source.now = func() time.Time { return now }
	if token, _ := source.Token(); token.AccessToken != "old" {
		t.Fatalf("fresh token was replaced: %s", token.AccessToken)
	}
	source.now = func() time.Time { return now.Add(10 * time.Hour) }
	token, err := source.Token()
	if err != nil || token.AccessToken != "access-1" || token.RefreshToken != "refresh" {
		t.Fatalf("after expiry by wall clock: %+v, %v", token, err)
	}
	if err := source.RefreshToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token, _ := source.Token(); token.AccessToken != "access-2" {
		t.Fatalf("forced refresh gave %s", token.AccessToken)
	}
}
