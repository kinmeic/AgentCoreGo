package server

import (
	"agent-core/internal/agent"
	"agent-core/internal/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type acceptedRun struct {
	CommandID      string `json:"command_id"`
	RunID          string `json:"run_id"`
	ConversationID string `json:"conversation_id"`
}

// Admission is durable before acknowledgement. Retrying a command ID reads the
// same run instead of re-executing tools. A changed payload is a conflict.
func (s *Server) acceptRun(req model.ChatRequest, commandID string) (*acceptedRun, error) {
	if strings.TrimSpace(req.Message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	if commandID == "" {
		commandID = uuid.NewString()
	}
	if len(commandID) > 128 || strings.ContainsAny(commandID, "\x00\r\n") {
		return nil, fmt.Errorf("invalid command_id")
	}
	wire, _ := json.Marshal(req)
	sum := sha256.Sum256(wire)
	fingerprint := hex.EncodeToString(sum[:])
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	previous, err := s.store.AcceptedCommand(commandID)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if previous.Fingerprint != fingerprint {
			return nil, fmt.Errorf("command_id already used with a different request")
		}
		return &acceptedRun{CommandID: commandID, RunID: previous.RunID, ConversationID: previous.ConversationID}, nil
	}
	if s.backgroundCtx.Err() != nil {
		return nil, fmt.Errorf("server is shutting down")
	}
	if len(s.backgroundRuns) >= s.cfg.Server.MaxConcurrentRuns {
		return nil, agent.ErrTooManyRuns
	}
	conversationID := req.ConversationID
	if conversationID == "" {
		conversationID = uuid.NewString()
		now := time.Now()
		if err := s.store.CreateConversation(&model.Conversation{ID: conversationID, Source: req.Source, Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now}); err != nil {
			return nil, err
		}
	} else {
		conversation, err := s.store.GetConversation(conversationID)
		if err != nil {
			return nil, err
		}
		if conversation == nil {
			return nil, agent.ErrConversationNotFound
		}
		if conversation.ArchivedAt != nil {
			return nil, agent.ErrConversationArchived
		}
	}
	runID := uuid.NewString()
	if err := s.store.SaveAcceptedCommand(commandID, fingerprint, runID, conversationID, req.Message, req.Source); err != nil {
		return nil, err
	}
	req.ConversationID = conversationID
	runCtx, cancel := context.WithCancel(s.backgroundCtx)
	s.backgroundRuns[runID] = cancel
	s.backgroundWG.Add(1)
	go func() {
		defer s.backgroundWG.Done()
		defer cancel()
		defer func() { s.backgroundMu.Lock(); delete(s.backgroundRuns, runID); s.backgroundMu.Unlock() }()
		_, err := s.orchestrator.StartAcceptedRun(runCtx, req, runID)
		if err != nil {
			s.finishAdmissionError(runID, conversationID, err)
		}
	}()
	return &acceptedRun{CommandID: commandID, RunID: runID, ConversationID: conversationID}, nil
}

func (s *Server) finishAdmissionError(runID, conversationID string, runErr error) {
	run, err := s.store.GetRun(runID)
	if err != nil || run == nil || run.FinishedAt != nil {
		return
	}
	now := time.Now()
	run.Status = model.RunStatusFailed
	run.Error = runErr.Error()
	run.FinishedAt = &now
	eventType, code := "error", "run_failed"
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		run.Status = model.RunStatusCancelled
		eventType, code = "cancelled", "cancelled"
	}
	_ = s.store.SaveRun(run)
	sequence, _ := s.store.LastRunEventSequence(runID)
	event := model.StreamEvent{SchemaVersion: 1, EventID: uuid.NewString(), Sequence: sequence + 1, RunID: runID, ConversationID: conversationID, Type: eventType, ErrorCode: code, Message: runErr.Error(), Timestamp: now}
	payload, _ := json.Marshal(event)
	_ = s.store.AppendRunEvent(&model.RunEvent{EventID: event.EventID, RunID: runID, ConversationID: conversationID, Sequence: event.Sequence, Type: event.Type, Payload: payload, CreatedAt: now})
}

func (s *Server) stopAcceptedRun(id string) bool {
	stopped := s.orchestrator.StopRun(id)
	s.backgroundMu.Lock()
	cancel := s.backgroundRuns[id]
	s.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return stopped
}
