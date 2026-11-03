package store

import (
	"agent-core/internal/model"
)

// ListRuns lists direct children, or root runs when parentRunID is empty.
func (s *SQLite) ListRuns(parentRunID string, limit int) ([]*model.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, conversation_id, status, input, source, metadata,
		iteration, reply, error, usage, created_at, updated_at, finished_at,
		parent_run_id, root_run_id, depth, kind, spec FROM runs
		WHERE COALESCE(parent_run_id, '') = ? ORDER BY created_at DESC LIMIT ?`, parentRunID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*model.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ConversationRuns lists all runs in one conversation.
func (s *SQLite) ConversationRuns(conversationID string, limit int) ([]*model.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, conversation_id, status, input, source, metadata,
		iteration, reply, error, usage, created_at, updated_at, finished_at,
		parent_run_id, root_run_id, depth, kind, spec FROM runs
		WHERE conversation_id = ? ORDER BY created_at DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*model.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
