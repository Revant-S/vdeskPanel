package drive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"vault/internal/drive/drivefake"
	"vault/internal/plaintextcheck"
)

func TestTransientFailuresAreRetried(t *testing.T) {
	cases := []struct {
		status int
		reason string
	}{
		{http.StatusForbidden, "userRateLimitExceeded"},
		{http.StatusForbidden, "rateLimitExceeded"},
		{http.StatusTooManyRequests, "rateLimitExceeded"},
		{http.StatusInternalServerError, "backendError"},
		{http.StatusBadGateway, "backendError"},
		{http.StatusServiceUnavailable, "backendError"},
	}
	for _, tc := range cases {
		server, client := newFakeAndClient(t)
		folder := createTestFolder(t, client, "", "folder")
		server.InjectFault(drivefake.Fault{Status: tc.status, Reason: tc.reason, Times: 3})
		before := len(server.Requests())
		if _, err := client.Get(context.Background(), folder.ID); err != nil {
			t.Fatalf("%d %s: %v", tc.status, tc.reason, err)
		}
		if calls := len(server.Requests()) - before; calls != 4 {
			t.Fatalf("%d %s: %d calls, want 4", tc.status, tc.reason, calls)
		}
	}
}

func TestPermissionDeniedIsNotRetried(t *testing.T) {
	server, client := newFakeAndClient(t)
	folder := createTestFolder(t, client, "", "folder")
	server.InjectFault(drivefake.Fault{Status: http.StatusForbidden, Reason: "insufficientFilePermissions", Times: 5})
	before := len(server.Requests())
	_, err := client.Get(context.Background(), folder.ID)
	if !errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrPermissionDenied", err)
	}
	if calls := len(server.Requests()) - before; calls != 1 {
		t.Fatalf("%d calls, want 1", calls)
	}
}

func TestRetriesStopAfterMaxAttempts(t *testing.T) {
	server, client := newFakeAndClient(t)
	server.InjectFault(drivefake.Fault{Status: http.StatusTooManyRequests, Reason: "rateLimitExceeded", Times: 100})
	_, err := client.StartPageToken(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
	if calls := len(server.Requests()); calls != 8 {
		t.Fatalf("%d calls, want 8", calls)
	}
}

func TestUnauthorizedRefreshesTokenOnce(t *testing.T) {
	server := drivefake.New()
	refresher := &countingRefresher{}
	client := newTestClient(t, server, Deps{TokenRefresher: refresher})
	server.InjectFault(drivefake.Fault{Status: http.StatusUnauthorized, Reason: "authError"})
	if _, err := client.StartPageToken(context.Background()); err != nil {
		t.Fatalf("after one 401: %v", err)
	}
	if refresher.calls != 1 {
		t.Fatalf("%d refreshes, want 1", refresher.calls)
	}

	server.InjectFault(drivefake.Fault{Status: http.StatusUnauthorized, Reason: "authError", Times: 5})
	before := len(server.Requests())
	_, err := client.StartPageToken(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
	if refresher.calls != 2 || len(server.Requests())-before != 2 {
		t.Fatalf("%d refreshes and %d calls, want one refresh and 2 calls", refresher.calls-1, len(server.Requests())-before)
	}
}

func TestRetryLogsCarryNoNames(t *testing.T) {
	server := drivefake.New()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(t, server, Deps{Logger: logger})
	server.InjectFault(drivefake.Fault{Status: http.StatusServiceUnavailable, Times: 2})
	content := []byte("secret body")
	if _, err := client.CreateSmallFile(context.Background(), NewFile{Name: "secret-name"}, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	if logs.Len() == 0 {
		t.Fatal("expected retry logs")
	}
	plaintextcheck.AssertBytesClean(t, "logs", logs.Bytes(), "secret-name", "secret body")
}

func TestLastContactCountsErrorAnswersButNotNetworkFailures(t *testing.T) {
	server, client := newFakeAndClient(t)
	if !client.LastContact().IsZero() {
		t.Fatal("contact before any request")
	}
	server.InjectFault(drivefake.Fault{Status: http.StatusForbidden, Reason: "insufficientFilePermissions", Times: 1})
	client.StartPageToken(context.Background())
	if client.LastContact().IsZero() {
		t.Fatal("an error answer from Drive is still contact")
	}

	offline := newTestClient(t, server, Deps{HTTPClient: &http.Client{Transport: &failingTransport{}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	offline.StartPageToken(ctx)
	if !offline.LastContact().IsZero() {
		t.Fatal("a failed connection counted as contact")
	}
}

type failingTransport struct{}

func (*failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network is unreachable")
}
