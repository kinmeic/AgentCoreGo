package server

import (
	"agent-core/internal/model"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleRunEventStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if run, err := s.store.GetRun(runID); err != nil || run == nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	cursor, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if cursor == 0 && r.Header.Get("Last-Event-ID") != "" {
		cursor = s.sequenceForEvent(runID, r.Header.Get("Last-Event-ID"))
	}
	events := make(chan model.StreamEvent, 128)
	go s.streamRunEvents(r, runID, cursor, events)
	s.serveEventStream(w, r, events)
}

func (s *Server) sequenceForEvent(runID, eventID string) uint64 {
	var cursor uint64
	for {
		events, err := s.store.RunEvents(runID, cursor, 1000)
		if err != nil || len(events) == 0 {
			return 0
		}
		for _, event := range events {
			if event.EventID == eventID {
				return event.Sequence
			}
			cursor = event.Sequence
		}
		if len(events) < 1000 {
			return 0
		}
	}
}

func (s *Server) streamRunEvents(r *http.Request, runID string, cursor uint64, output chan<- model.StreamEvent) {
	defer close(output)
	for r.Context().Err() == nil {
		events, err := s.store.RunEvents(runID, cursor, 1000)
		if err != nil {
			_ = sendStreamEvent(r, output, model.StreamEvent{Type: "error", ErrorCode: "event_store_failed", Message: "failed to read run events"})
			return
		}
		for _, record := range events {
			var event model.StreamEvent
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				_ = sendStreamEvent(r, output, model.StreamEvent{Type: "resync_required", ErrorCode: "invalid_event_payload", RunID: runID})
				return
			}
			if !sendStreamEvent(r, output, event) {
				return
			}
			cursor = record.Sequence
		}
		last, err := s.store.LastRunEventSequence(runID)
		if err != nil {
			_ = sendStreamEvent(r, output, model.StreamEvent{Type: "error", ErrorCode: "event_store_failed", RunID: runID})
			return
		}
		if cursor > last {
			_ = sendStreamEvent(r, output, model.StreamEvent{Type: "resync_required", ErrorCode: "cursor_beyond_watermark", RunID: runID})
			return
		}
		if cursor >= last {
			if run, _ := s.store.GetRun(runID); run != nil && run.FinishedAt != nil {
				// Completion updates the run row before appending the terminal event.
				// Wait for that durable event instead of dropping it at the finish boundary.
				if last > 0 {
					tail, _ := s.store.RunEvents(runID, last-1, 1)
					if len(tail) > 0 {
						switch tail[0].Type {
						case "done", "error", "cancelled":
							if cursor >= tail[0].Sequence {
								return
							}
						}
					}
				}
				if time.Since(*run.FinishedAt) > 2*time.Second {
					_ = sendStreamEvent(r, output, model.StreamEvent{Type: "resync_required", ErrorCode: "terminal_event_unavailable", RunID: runID})
					return
				}
			}
			if len(events) == 1000 {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func sendStreamEvent(r *http.Request, output chan<- model.StreamEvent, event model.StreamEvent) bool {
	select {
	case output <- event:
		return true
	case <-r.Context().Done():
		return false
	case <-time.After(2 * time.Second):
		select {
		case output <- model.StreamEvent{Type: "resync_required", ErrorCode: "slow_consumer", RunID: event.RunID, ConversationID: event.ConversationID}:
		default:
		}
		return false
	}
}
