package store

import (
	"agent-core/internal/model"
	"database/sql"
	"time"
)

type AcceptedCommand struct{ Fingerprint, RunID, ConversationID string }

func (s *SQLite) AcceptedCommand(id string) (*AcceptedCommand, error) {
	var command AcceptedCommand
	err := s.db.QueryRow(`SELECT fingerprint,run_id,conversation_id FROM accepted_commands WHERE command_id=?`, id).Scan(&command.Fingerprint, &command.RunID, &command.ConversationID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &command, err
}

// SaveAcceptedCommand atomically stores admission and a queryable pending run.
func (s *SQLite) SaveAcceptedCommand(id, fingerprint, runID, conversationID, input, source string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO accepted_commands(command_id,fingerprint,run_id,conversation_id) VALUES(?,?,?,?)`, id, fingerprint, runID, conversationID); err != nil {
		return err
	}
	now := time.Now()
	if _, err = tx.Exec(`INSERT INTO runs(id,conversation_id,status,input,source,created_at,updated_at,spec,metadata,usage,reply,error) VALUES(?,?,?,?,?,?,?,'{}','{}','{}','','')`, runID, conversationID, model.RunStatusPending, input, source, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Pending admissions cannot be replayed automatically after a process crash.
// Tools may already have side effects; users explicitly resume a checkpoint.
func (s *SQLite) InterruptAcceptedCommands() error {
	_, err := s.db.Exec(`UPDATE runs SET status='failed',error='server restarted; inspect checkpoint and explicitly resume',finished_at=?,updated_at=? WHERE finished_at IS NULL AND id IN (SELECT run_id FROM accepted_commands)`, time.Now(), time.Now())
	return err
}
