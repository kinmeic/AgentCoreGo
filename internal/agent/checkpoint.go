package agent

import (
	"agent-core/internal/model"
	"encoding/json"
	"log"
	"time"
)

// Checkpoint captures the state of an agent run for later resumption.
type Checkpoint struct {
	ConversationID string         `json:"conversation_id"`
	Iteration      int            `json:"iteration"`
	Messages       []model.Message `json:"messages"`
	Actions        []model.Action `json:"actions"`
	PromptTokens   int            `json:"prompt_tokens"`
	ComplTokens    int            `json:"completion_tokens"`
	TotalTokens    int            `json:"total_tokens"`
	SavedAt        time.Time      `json:"saved_at"`
	Completed      bool           `json:"completed"`
}

// CheckpointStore provides persistence for checkpoints.
type CheckpointStore interface {
	SaveCheckpoint(cp *Checkpoint) error
	LoadCheckpoint(convID string) (*Checkpoint, error)
	DeleteCheckpoint(convID string) error
}

// SQLiteCheckpointStore implements CheckpointStore using SQLite.
type SQLiteCheckpointStore struct {
	store checkpointDB
}

type checkpointDB interface {
	SaveCheckpointData(convID string, data []byte) error
	LoadCheckpointData(convID string) ([]byte, error)
	DeleteCheckpointData(convID string) error
}

// NewSQLiteCheckpointStore creates a checkpoint store backed by SQLite.
func NewSQLiteCheckpointStore(db checkpointDB) *SQLiteCheckpointStore {
	return &SQLiteCheckpointStore{store: db}
}

func (s *SQLiteCheckpointStore) SaveCheckpoint(cp *Checkpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return s.store.SaveCheckpointData(cp.ConversationID, data)
}

func (s *SQLiteCheckpointStore) LoadCheckpoint(convID string) (*Checkpoint, error) {
	data, err := s.store.LoadCheckpointData(convID)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (s *SQLiteCheckpointStore) DeleteCheckpoint(convID string) error {
	return s.store.DeleteCheckpointData(convID)
}

// saveCheckpoint is a helper that saves a checkpoint after a tool execution.
// Tool results are truncated to prevent large checkpoint payloads.
func (o *Orchestrator) saveCheckpoint(convID string, iteration int, messages []model.Message, actions []model.Action, usage *model.UsageInfo) {
	if o.checkpointStore == nil {
		return
	}

	// Create a copy with truncated tool results to keep checkpoint size manageable
	truncatedMsgs := make([]model.Message, len(messages))
	copy(truncatedMsgs, messages)
	for i := range truncatedMsgs {
		if truncatedMsgs[i].Role == model.RoleTool && len(truncatedMsgs[i].Content) > 5000 {
			runes := []rune(truncatedMsgs[i].Content)
			if len(runes) > 5000 {
				truncatedMsgs[i].Content = string(runes[:5000]) + "\n... [truncated for checkpoint]"
			}
		}
	}

	cp := &Checkpoint{
		ConversationID: convID,
		Iteration:      iteration,
		Messages:       truncatedMsgs,
		Actions:        actions,
		SavedAt:        time.Now(),
	}
	if usage != nil {
		cp.PromptTokens = usage.PromptTokens
		cp.ComplTokens = usage.CompletionTokens
		cp.TotalTokens = usage.TotalTokens
	}

	if err := o.checkpointStore.SaveCheckpoint(cp); err != nil {
		log.Printf("[agent] Failed to save checkpoint: %v", err)
	}
}

// loadCheckpoint attempts to load a checkpoint for a conversation.
func (o *Orchestrator) loadCheckpoint(convID string) *Checkpoint {
	if o.checkpointStore == nil {
		return nil
	}

	cp, err := o.checkpointStore.LoadCheckpoint(convID)
	if err != nil {
		log.Printf("[agent] Failed to load checkpoint: %v", err)
		return nil
	}
	return cp
}
