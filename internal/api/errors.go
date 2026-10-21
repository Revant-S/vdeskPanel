package api

import (
	"errors"
	"net/http"
)

// ResponseError is a non-2xx answer from the daemon. It unwraps to the matching sentinel
// from service.go, so callers test it with errors.Is.
type ResponseError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ResponseError) Error() string {
	return e.Message
}

func (e *ResponseError) Unwrap() error {
	return sentinelForCode(e.Code)
}

type errorMapping struct {
	sentinel error
	code     string
	status   int
}

func errorMappings() []errorMapping {
	return []errorMapping{
		{ErrWrongPassword, CodeWrongPassword, http.StatusUnauthorized},
		{ErrLocked, CodeLocked, http.StatusLocked},
		{ErrNotFound, CodeNotFound, http.StatusNotFound},
		{ErrBusy, CodeBusy, http.StatusConflict},
		{ErrNewerFormat, CodeNewerFormat, http.StatusConflict},
		{ErrInvalidRequest, CodeInvalidRequest, http.StatusBadRequest},
	}
}

// errorResponse returns the status and body for err. Unknown errors become a bare 500, so
// internal details stay in the daemon log.
func errorResponse(err error) (int, ErrorBody) {
	for _, mapping := range errorMappings() {
		if errors.Is(err, mapping.sentinel) {
			return mapping.status, ErrorBody{Error: err.Error(), Code: mapping.code}
		}
	}
	return http.StatusInternalServerError, ErrorBody{Error: "internal error"}
}

func sentinelForCode(code string) error {
	for _, mapping := range errorMappings() {
		if mapping.code == code {
			return mapping.sentinel
		}
	}
	return nil
}
