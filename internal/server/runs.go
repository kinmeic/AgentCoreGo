package server

import (
	"agent-core/internal/agent"
	"agent-core/internal/model"
	"agent-core/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.PathValue("id"))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load run")
		return
	}
	if run == nil {
		respondError(w, http.StatusNotFound, "run not found")
		return
	}
	respondJSON(w, http.StatusOK, run)
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stopped := s.stopAcceptedRun(id)
	if !stopped {
		run, err := s.store.GetRun(id)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to load run")
			return
		}
		if run == nil {
			respondError(w, http.StatusNotFound, "run not found")
			return
		}
		respondJSON(w, http.StatusConflict, map[string]any{"stopped": false, "status": run.Status})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"stopped": true, "status": "cancellation_requested"})
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Strategy string `json:"strategy"`
		Stream   bool   `json:"stream"`
	}
	if err := decodeBody(r, &body); err != nil && !errors.Is(err, io.EOF) {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	stream := body.Stream || r.URL.Query().Get("stream") == "true"
	if stream {
		runCtx, cancel := context.WithCancel(r.Context())
		defer cancel()
		events := make(chan model.StreamEvent, 64)
		go func() {
			defer close(events)
			response, err := s.orchestrator.ResumeRun(runCtx, r.PathValue("id"), body.Strategy, true, events)
			if err != nil && response == nil {
				events <- model.StreamEvent{Type: "error", ErrorCode: "resume_failed", Message: err.Error(), Timestamp: time.Now()}
			}
		}()
		s.serveEventStream(w, r, events)
		return
	}
	response, err := s.orchestrator.ResumeRun(r.Context(), r.PathValue("id"), body.Strategy, false, nil)
	if err != nil {
		respondRunError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, response)
}

func respondRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrRunNotFound), errors.Is(err, agent.ErrConversationNotFound):
		respondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agent.ErrConversationArchived), errors.Is(err, agent.ErrResumeToolRequired):
		respondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agent.ErrTooManyRuns):
		respondError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		respondError(w, http.StatusConflict, "run was cancelled")
	default:
		log.Printf("[server] run failed: %v", err)
		respondError(w, http.StatusInternalServerError, "agent run failed")
	}
}

func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.RunEvents(r.PathValue("id"), after, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load run events")
		return
	}
	if events == nil {
		events = []*model.RunEvent{}
	}
	respondJSON(w, http.StatusOK, events)
}

func (s *Server) handlePendingApprovals(w http.ResponseWriter, _ *http.Request) {
	requests := s.orchestrator.PendingApprovals()
	if requests == nil {
		requests = []tool.ApprovalRequest{}
	}
	respondJSON(w, http.StatusOK, requests)
}

func (s *Server) handleRespondApproval(w http.ResponseWriter, r *http.Request) {
	var response tool.ApprovalResponse
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&response); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if response.Decision != "allow" && response.Decision != "deny" {
		respondError(w, http.StatusBadRequest, "decision must be allow or deny")
		return
	}
	if response.Scope == "" {
		response.Scope = "once"
	}
	if response.Scope != "once" && response.Scope != "conversation" {
		respondError(w, http.StatusBadRequest, "scope must be once or conversation")
		return
	}
	if !s.orchestrator.RespondApproval(r.PathValue("id"), response) {
		respondError(w, http.StatusNotFound, "approval request not found or already settled")
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"delivered": true})
}
