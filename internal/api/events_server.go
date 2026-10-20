package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// streamEvents writes Server-Sent Events by hand: one data line per event and a comment
// as heartbeat is all the protocol needs here, which does not justify a dependency.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, fmt.Errorf("response writer cannot stream"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events := s.service.Subscribe(r.Context())
	heartbeat := time.NewTicker(s.heartbeatInterval)
	defer heartbeat.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case event, open := <-events:
			if !open {
				return
			}
			err = writeEvent(w, event)
		case <-heartbeat.C:
			_, err = io.WriteString(w, ": heartbeat\n\n")
		}
		if err != nil {
			return
		}
		flusher.Flush()
	}
}

func writeEvent(w io.Writer, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}
