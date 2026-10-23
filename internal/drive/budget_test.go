package drive

import (
	"context"
	"math"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"vault/internal/drive/drivefake"
)

func assertRate(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s: rate %.6f, want %.6f", label, got, want)
	}
}

func TestBucketHalvesThenRegainsTenPercentPerErrorFreeMinute(t *testing.T) {
	clock := newTestClock()
	bucket := newAdaptiveBucket(3, 10, clock.now)
	bucket.penalize()
	assertRate(t, "after penalty", bucket.currentRate(), 1.5)
	clock.advance(59 * time.Second)
	assertRate(t, "before a full minute", bucket.currentRate(), 1.5)
	clock.advance(time.Second)
	assertRate(t, "after one minute", bucket.currentRate(), 1.65)
	clock.advance(time.Minute)
	assertRate(t, "after two minutes", bucket.currentRate(), 1.815)
	bucket.penalize()
	assertRate(t, "second penalty", bucket.currentRate(), 0.9075)
	clock.advance(30 * time.Second)
	assertRate(t, "penalty restarts the minute", bucket.currentRate(), 0.9075)
	clock.advance(time.Hour)
	assertRate(t, "capped at ceiling", bucket.currentRate(), 3)
}

func TestBucketNeverDropsBelowItsFloor(t *testing.T) {
	bucket := newAdaptiveBucket(3, 10, newTestClock().now)
	for range 20 {
		bucket.penalize()
	}
	assertRate(t, "floor", bucket.currentRate(), 3.0/16)
}

func TestRateLimitAnswersPenalizeOnlyTheirBucket(t *testing.T) {
	for _, fault := range []drivefake.Fault{
		{Status: http.StatusTooManyRequests, Reason: "rateLimitExceeded"},
		{Status: http.StatusForbidden, Reason: "userRateLimitExceeded"},
		{Status: http.StatusForbidden, Reason: "rateLimitExceeded"},
	} {
		server := drivefake.New()
		client := newTestClient(t, server, Deps{Now: newTestClock().now})
		fault.Match = matchMethodAndPath(http.MethodPost, "/drive/v3/files")
		server.InjectFault(fault)
		createTestFolder(t, client, "", "folder")
		assertRate(t, "metadata writes", client.Budget().MetadataWriteRate(), 5e5)
		assertRate(t, "reads", client.Budget().ReadRate(), 1e6)
	}
}

func TestServerErrorsDoNotPenalizeTheBudget(t *testing.T) {
	server := drivefake.New()
	client := newTestClient(t, server, Deps{Now: newTestClock().now})
	server.InjectFault(drivefake.Fault{Status: http.StatusInternalServerError, Times: 2})
	createTestFolder(t, client, "", "folder")
	assertRate(t, "metadata writes", client.Budget().MetadataWriteRate(), 1e6)
}

func TestMetadataWritesAreHeldToTheirSustainedRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := drivefake.New()
		cfg := testConfig(server)
		cfg.Budget = DefaultBudgetConfig()
		client, err := NewClient(context.Background(), cfg, Deps{HTTPClient: server.HTTPClient()})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		for range 16 {
			createTestFolder(t, client, "", "folder")
		}
		// A burst of 10 is free; the remaining 6 need 2 s at 3/s.
		if elapsed := time.Since(started); elapsed < 2*time.Second-time.Millisecond || elapsed > 2*time.Second+time.Millisecond {
			t.Fatalf("16 creates took %v, want 2s", elapsed)
		}
	})
}

func TestFullJitterBackOffStaysWithinCap(t *testing.T) {
	backOff := newFullJitterBackOff(DefaultRetryConfig())
	for round := range 200 {
		backOff.Reset()
		for attempt := range 12 {
			ceiling := min(time.Second<<attempt, 64*time.Second)
			delay := backOff.NextBackOff()
			if delay < 0 || delay > ceiling {
				t.Fatalf("round %d attempt %d: delay %v outside [0, %v]", round, attempt, delay, ceiling)
			}
		}
	}
}

func TestRetryDelaysAreBoundedByTheSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := drivefake.New()
		cfg := testConfig(server)
		cfg.Retry = DefaultRetryConfig()
		client, err := NewClient(context.Background(), cfg, Deps{HTTPClient: server.HTTPClient()})
		if err != nil {
			t.Fatal(err)
		}
		server.InjectFault(drivefake.Fault{Status: http.StatusServiceUnavailable, Times: 3})
		started := time.Now()
		if _, err := client.StartPageToken(context.Background()); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed > 7*time.Second {
			t.Fatalf("three retries took %v, want at most 1s+2s+4s", elapsed)
		}
	})
}
