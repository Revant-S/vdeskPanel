package drive

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"vault/internal/drive/drivefake"
)

// hangOnce stands in for a connection that died without closing: the first request never
// gets an answer until its context gives up.
type hangOnce struct {
	next  http.RoundTripper
	calls atomic.Int32
}

func (h *hangOnce) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.calls.Add(1) == 1 {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return h.next.RoundTrip(r)
}

func TestHungRequestTimesOutAndRetries(t *testing.T) {
	server := drivefake.New()
	config := testConfig(server)
	config.Retry.AttemptTimeout = 200 * time.Millisecond
	transport := &hangOnce{next: server.HTTPClient().Transport}
	client, err := NewClient(context.Background(), config, Deps{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.StartPageToken(ctx); err != nil {
		t.Fatalf("request did not recover from a hung attempt: %v", err)
	}
	if transport.calls.Load() < 2 {
		t.Fatal("no retry after the hung attempt")
	}
}
