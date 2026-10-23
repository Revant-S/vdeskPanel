package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cosmoboy/vault/internal/secret"
)

// maxPasswordBytes caps password bodies (R20). net/http's own read buffers may still hold
// a copy that cannot be wiped; that limit is documented in the step notes.
const maxPasswordBytes = 1 << 10

var errPasswordTooLarge = errors.New("password body is larger than 1 KiB")

// handlePassword reads a raw octet-stream password into a buffer of fixed size, so no
// growing slice leaves copies behind, and wipes it once the service call returns.
func (s *Server) handlePassword(pattern string, use func(r *http.Request, password []byte) error) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		buffer := s.newPasswordBuffer()
		defer secret.Wipe(buffer)
		s.respond(w, r, nil, usePassword(w, r, buffer, use))
	})
}

func usePassword(w http.ResponseWriter, r *http.Request, buffer []byte, use func(*http.Request, []byte) error) error {
	length, err := readPassword(w, r, buffer)
	if err != nil {
		return err
	}
	if length == 0 {
		return fmt.Errorf("%w: empty password", ErrInvalidRequest)
	}
	return use(r, buffer[:length])
}

func readPassword(w http.ResponseWriter, r *http.Request, buffer []byte) (int, error) {
	if r.ContentLength > int64(len(buffer)) {
		return 0, fmt.Errorf("%w: %w", ErrInvalidRequest, errPasswordTooLarge)
	}
	body := http.MaxBytesReader(w, r.Body, int64(len(buffer)))
	length, err := io.ReadFull(body, buffer)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("%w: read password: %w", ErrInvalidRequest, err)
	}
	if length < len(buffer) {
		return length, nil
	}
	var extra [1]byte
	_, err = io.ReadFull(body, extra[:])
	secret.Wipe(extra[:])
	if !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("%w: %w", ErrInvalidRequest, errPasswordTooLarge)
	}
	return length, nil
}
