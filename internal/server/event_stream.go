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
	for {
		events, err := s.store.RunEvents(runID, cursor, 1000)
		if err != nil {
			_ = sendStreamEvent(r, output, model.StreamEvent{Type: "error", ErrorCode: "event_store_failed", Message: "failed to read run events"})
			return
		}
		for _, record := range events {
			var event model.StreamEvent
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				continue
			}
			if !sendStreamEvent(r, output, event) {
				return
			}
			cursor = event.Sequence
		}
		last, err := s.store.LastRunEventSequence(runID)
		if err == nil && cursor > last {
			_ = sendStreamEvent(r, output, model.StreamEvent{Type: "resync_required", ErrorCode: "cursor_beyond_watermark", RunID: runID})
			return
		}
		if err == nil && cursor >= last {
			if run, _ := s.store.GetRun(runID); run != nil && run.FinishedAt != nil {
				return
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
