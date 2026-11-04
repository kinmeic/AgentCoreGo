package store

import (
	"agent-core/internal/model"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrConversationActive   = errors.New("conversation has active runs")
)

// PermanentlyDeleteConversation removes one conversation and its directly related
// online database records. Active runs are rejected so they cannot keep writing
// messages or checkpoints after the conversation has been deleted.
func (s *SQLite) PermanentlyDeleteConversation(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin conversation deletion: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM conversations WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConversationNotFound
		}
		return fmt.Errorf("find conversation: %w", err)
	}

	var activeRunID string
	err = tx.QueryRow(`SELECT id FROM runs
		WHERE conversation_id = ? AND status IN (?, ?, ?, ?) LIMIT 1`,
		id, model.RunStatusPending, model.RunStatusRunning,
		model.RunStatusWaitingClarify, model.RunStatusWaitingApproval).Scan(&activeRunID)
	if err == nil {
		return fmt.Errorf("%w: %s", ErrConversationActive, activeRunID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check conversation activity: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM accepted_commands WHERE conversation_id = ?`, id); err != nil {
		return fmt.Errorf("delete accepted commands: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM workflow_runs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete workflow run: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("verify conversation deletion: %w", err)
	}
	if deleted != 1 {
		return ErrConversationNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation deletion: %w", err)
	}
	return nil
}
