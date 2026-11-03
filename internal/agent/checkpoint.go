package agent

import (
	"encoding/json"
	"time"

	"agent-core/internal/llm"
	"agent-core/internal/model"
	"agent-core/internal/tool"
)

// Checkpoint is a run-scoped resumable boundary with an immutable execution manifest.
type Checkpoint struct {
	ConversationID string               `json:"conversation_id"`
	RunID          string               `json:"run_id"`
	Spec           model.RunSpec        `json:"spec"`
	Iteration      int                  `json:"iteration"`
	Messages       []model.Message      `json:"messages"`
	Actions        []model.Action       `json:"actions"`
	Pending        []model.ToolCall     `json:"pending_tool_calls,omitempty"`
	InFlight       *model.ToolCall      `json:"in_flight_tool_call,omitempty"`
	Discovered     []string             `json:"discovered_tools,omitempty"`
	Discovery      *tool.DiscoveryState `json:"-"`
	Provider       llm.Provider         `json:"-"`
	PromptTokens   int                  `json:"prompt_tokens"`
	ComplTokens    int                  `json:"completion_tokens"`
	TotalTokens    int                  `json:"total_tokens"`
	SavedAt        time.Time            `json:"saved_at"`
	Completed      bool                 `json:"completed"`
}

type CheckpointStore interface {
	SaveCheckpoint(cp *Checkpoint) error
	LoadCheckpoint(runID, conversationID string) (*Checkpoint, error)
	DeleteCheckpoint(runID string) error
}

type SQLiteCheckpointStore struct{ store checkpointDB }

type checkpointDB interface {
	SaveCheckpointData(runID, conversationID string, data []byte) error
	LoadCheckpointData(runID string) ([]byte, error)
	LoadLegacyCheckpointData(conversationID string) ([]byte, error)
	DeleteCheckpointData(runID string) error
}

func NewSQLiteCheckpointStore(db checkpointDB) *SQLiteCheckpointStore {
	return &SQLiteCheckpointStore{store: db}
}

func (s *SQLiteCheckpointStore) SaveCheckpoint(cp *Checkpoint) error {
	cp.SavedAt = time.Now()
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return s.store.SaveCheckpointData(cp.RunID, cp.ConversationID, data)
}

func (s *SQLiteCheckpointStore) LoadCheckpoint(runID, conversationID string) (*Checkpoint, error) {
	data, err := s.store.LoadCheckpointData(runID)
	if err != nil {
		return nil, err
	}
	legacy := false
	if data == nil {
		data, err = s.store.LoadLegacyCheckpointData(conversationID)
		legacy = true
		if err != nil || data == nil {
			return nil, err
		}
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	if cp.RunID != "" && cp.RunID != runID {
		return nil, nil
	}
	if legacy {
		cp.RunID = runID
	}
	return &cp, nil
}

func (s *SQLiteCheckpointStore) DeleteCheckpoint(runID string) error {
	return s.store.DeleteCheckpointData(runID)
}
