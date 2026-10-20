// Package drive talks to the Google Drive v3 API within the app's self-imposed request budget.
package drive

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrNotFound             = errors.New("drive: not found")
	ErrAlreadyExists        = errors.New("drive: already exists")
	ErrPermissionDenied     = errors.New("drive: permission denied")
	ErrUnauthorized         = errors.New("drive: unauthorized")
	ErrRateLimited          = errors.New("drive: rate limited")
	ErrPageTokenExpired     = errors.New("drive: page token expired")
	ErrUploadSessionExpired = errors.New("drive: upload session expired")
)

const (
	reasonUserRateLimit = "userRateLimitExceeded"
	reasonRateLimit     = "rateLimitExceeded"
	reasonInvalid       = "invalid"
)

// APIError is a non-2xx answer from Drive, reduced to the parts the client acts on.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("drive: status %d (%s): %s", e.Status, e.Reason, e.Message)
}

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrAlreadyExists:
		return e.Status == http.StatusConflict
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrRateLimited:
		return e.isRateLimit()
	case ErrPermissionDenied:
		return e.Status == http.StatusForbidden && !e.isRateLimit()
	}
	return false
}

// Drive reports quota exhaustion as 403 with a rate-limit reason, which must not be
// confused with a 403 for missing permission.
func (e *APIError) isRateLimit() bool {
	if e.Status == http.StatusTooManyRequests {
		return true
	}
	return e.Status == http.StatusForbidden && (e.Reason == reasonUserRateLimit || e.Reason == reasonRateLimit)
}
