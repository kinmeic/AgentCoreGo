package agent

import (
	"encoding/json"
	"time"

	"agent-core/internal/model"
	"agent-core/internal/tool"
)

// Checkpoint captures a resumable boundary. InFlight identifies a tool whose
// result was not committed; side-effecting tools require an explicit resume strategy.
type Checkpoint struct {
	ConversationID string               `json:"conversation_id"`
	RunID          string               `json:"run_id"`
	Iteration      int                  `json:"iteration"`
	Messages       []model.Message      `json:"messages"`
	Actions        []model.Action       `json:"actions"`
	Pending        []model.ToolCall     `json:"pending_tool_calls,omitempty"`
	InFlight       *model.ToolCall      `json:"in_flight_tool_call,omitempty"`
	Discovered     []string             `json:"discovered_tools,omitempty"`
	Discovery      *tool.DiscoveryState `json:"-"`
	PromptTokens   int                  `json:"prompt_tokens"`
	ComplTokens    int                  `json:"completion_tokens"`
	TotalTokens    int                  `json:"total_tokens"`
	SavedAt        time.Time            `json:"saved_at"`
	Completed      bool                 `json:"completed"`
}

// CheckpointStore provides persistence for checkpoints.
type CheckpointStore interface {
	SaveCheckpoint(cp *Checkpoint) error
	LoadCheckpoint(convID string) (*Checkpoint, error)
	DeleteCheckpoint(convID string) error
}

// SQLiteCheckpointStore implements CheckpointStore using SQLite.
type SQLiteCheckpointStore struct{ store checkpointDB }

type checkpointDB interface {
	SaveCheckpointData(convID string, data []byte) error
	LoadCheckpointData(convID string) ([]byte, error)
	DeleteCheckpointData(convID string) error
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
	return s.store.SaveCheckpointData(cp.ConversationID, data)
}

func (s *SQLiteCheckpointStore) LoadCheckpoint(convID string) (*Checkpoint, error) {
	data, err := s.store.LoadCheckpointData(convID)
	if err != nil || data == nil {
		return nil, err
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
