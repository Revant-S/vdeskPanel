package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	maxJSONBody              = 64 << 10
	defaultHeartbeatInterval = 15 * time.Second
)

// Server answers the routes in routes.md on behalf of a Service.
type Server struct {
	service           Service
	logger            *slog.Logger
	mux               *http.ServeMux
	httpServer        *http.Server
	heartbeatInterval time.Duration
	newPasswordBuffer func() []byte
	closing           chan struct{}
	closeOnce         sync.Once
}

// handlerFunc returns the value to send as JSON, or nil for 204 No Content.
type handlerFunc func(r *http.Request) (any, error)

func NewServer(service Service, logger *slog.Logger) *Server {
	s := &Server{
		service:           service,
		logger:            logger,
		mux:               http.NewServeMux(),
		heartbeatInterval: defaultHeartbeatInterval,
		newPasswordBuffer: func() []byte { return make([]byte, maxPasswordBytes) },
		closing:           make(chan struct{}),
	}
	s.httpServer = &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

// Serve answers requests on listener until Shutdown. It returns nil after a clean shutdown.
func (s *Server) Serve(listener net.Listener) error {
	err := s.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve api: %w", err)
}

// Shutdown ends event streams first, since an open stream never goes idle and would hold
// the graceful shutdown until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.closing) })
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down api: %w", err)
	}
	return nil
}

func (s *Server) handle(pattern string, handler handlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		result, err := handler(r)
		s.respond(w, r, result, err)
	})
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, result any, err error) {
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if result == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, body := errorResponse(err)
	if status == http.StatusInternalServerError {
		s.logger.Error("api request failed", "route", r.Pattern, "err", err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// handleJSON decodes the request body into T before calling handler. It is a function
// rather than a method because methods cannot take type parameters.
func handleJSON[T any](s *Server, pattern string, handler func(r *http.Request, body T) (any, error)) {
	s.handle(pattern, func(r *http.Request) (any, error) {
		var body T
		if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&body); err != nil {
			return nil, fmt.Errorf("%w: decode body: %v", ErrInvalidRequest, err)
		}
		return handler(r, body)
	})
}
