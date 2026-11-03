package server

import (
	"agent-core/internal/model"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSEWriter writes replayable event IDs and protocol event names.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported by this response writer")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, flusher: flusher}, nil
}

func (s *SSEWriter) WriteEvent(event model.StreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if event.EventID != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", event.EventID); err != nil {
			return err
		}
	}
	if event.Type != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", event.Type); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteComment(comment string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", comment); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *Server) serveEventStream(w http.ResponseWriter, r *http.Request, events <-chan model.StreamEvent) {
	writer, err := NewSSEWriter(w)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := writer.WriteEvent(event); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := writer.WriteComment("keep-alive"); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
