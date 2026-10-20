package drivefake

import (
	"context"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Fault makes matching requests fail with Status and Reason, Times times (at least once).
// With AfterBytes set, an upload chunk keeps its first AfterBytes bytes and a download
// sends only AfterBytes bytes before the failure, like a connection cut mid-stream.
type Fault struct {
	Match      func(*http.Request) bool
	Status     int
	Reason     string
	Times      int
	AfterBytes int64
}

func (s *Server) InjectFault(fault Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fault.Times < 1 {
		fault.Times = 1
	}
	if fault.Status == 0 {
		fault.Status = http.StatusServiceUnavailable
	}
	s.faults = append(s.faults, &fault)
}

func (s *Server) takeFault(r *http.Request) *Fault {
	for i, fault := range s.faults {
		if fault.Match != nil && !fault.Match(r) {
			continue
		}
		fault.Times--
		if fault.Times == 0 {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
		}
		return fault
	}
	return nil
}

type cutKey struct{}

func withCut(r *http.Request, fault *Fault) *http.Request {
	if fault == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), cutKey{}, fault))
}

func cutFrom(r *http.Request) *Fault {
	fault, _ := r.Context().Value(cutKey{}).(*Fault)
	return fault
}

type quota struct {
	perMinute *rate.Limiter
	creates   *rate.Limiter
}

func newQuota(perMinute int, createsPerSecond float64, burst int) *quota {
	return &quota{
		perMinute: rate.NewLimiter(rate.Limit(float64(perMinute)/60), perMinute),
		creates:   rate.NewLimiter(rate.Limit(createsPerSecond), burst),
	}
}

func (q *quota) admit(r *http.Request, now time.Time) *apiError {
	if q == nil {
		return nil
	}
	if !q.perMinute.AllowN(now, 1) {
		return failure(http.StatusForbidden, "userRateLimitExceeded", "User rate limit exceeded.")
	}
	if isCreate(r) && !q.creates.AllowN(now, 1) {
		return failure(http.StatusTooManyRequests, "rateLimitExceeded", "Rate limit exceeded.")
	}
	return nil
}

func isCreate(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	return path == "/drive/v3/files" || path == "/upload/drive/v3/files" || strings.HasSuffix(path, "/copy")
}
