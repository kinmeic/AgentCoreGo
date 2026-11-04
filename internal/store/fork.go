package store

import (
	"agent-core/internal/model"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CloneConversation copies conversation metadata and legacy/message history into a new branch.
func (s *SQLite) CloneConversation(sourceID string) (*model.Conversation, error) {
	source, err := s.GetConversation(sourceID)
	if err != nil || source == nil {
		return nil, err
	}
	now := time.Now()
	clone := &model.Conversation{ID: uuid.NewString(), Title: source.Title + " (fork)", Source: source.Source,
		Metadata: map[string]string{"forked_from": sourceID}, CreatedAt: now, UpdatedAt: now}
	for key, value := range source.Metadata {
		clone.Metadata[key] = value
	}
	clone.Metadata["forked_from"] = sourceID
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	metadata, _ := json.Marshal(clone.Metadata)
	if _, err := tx.Exec(`INSERT INTO conversations(id, title, source, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, clone.ID, clone.Title, clone.Source, string(metadata), now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO conversation_logs(
			conversation_id, run_id, role, content, tool_calls, tool_call_id, status, intent, actions,
			prompt_tokens, completion_tokens, context_tokens, created_at, provider_state)
			SELECT ?, run_id, role, content, tool_calls, tool_call_id, status, intent, actions,
			prompt_tokens, completion_tokens, context_tokens, created_at, provider_state FROM conversation_logs
			WHERE conversation_id = ? ORDER BY id`, clone.ID, sourceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return clone, nil
}
