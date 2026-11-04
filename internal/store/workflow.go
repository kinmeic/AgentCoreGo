package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// WorkflowDefinitionRecord is the immutable definition and scheduler status.
type WorkflowDefinitionRecord struct {
	ID               string          `json:"id"`
	Definition       json.RawMessage `json:"definition"`
	DefinitionDigest string          `json:"definition_digest"`
	Status           string          `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// WorkflowNodeRecord is one node attempt and its idempotency evidence.
type WorkflowNodeRecord struct {
	RunID     string          `json:"run_id"`
	NodeID    string          `json:"node_id"`
	Attempt   int             `json:"attempt"`
	InputHash string          `json:"input_hash"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (s *SQLite) SaveWorkflow(record *WorkflowDefinitionRecord) error {
	_, err := s.db.Exec(`INSERT INTO workflow_runs(id, definition, definition_digest, status, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ? FROM conversations WHERE id = ?
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at`,
		record.ID, string(record.Definition), record.DefinitionDigest, record.Status, record.CreatedAt, time.Now(), record.ID)
	return err
}

func (s *SQLite) WorkflowDefinition(id string) (*WorkflowDefinitionRecord, error) {
	var record WorkflowDefinitionRecord
	var definition string
	err := s.db.QueryRow(`SELECT id, definition, definition_digest, status, created_at, updated_at
		FROM workflow_runs WHERE id = ?`, id).Scan(&record.ID, &definition, &record.DefinitionDigest,
		&record.Status, &record.CreatedAt, &record.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	record.Definition = json.RawMessage(definition)
	return &record, err
}

func (s *SQLite) ListWorkflows(limit int) ([]*WorkflowDefinitionRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, definition, definition_digest, status, created_at, updated_at
		FROM workflow_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*WorkflowDefinitionRecord
	for rows.Next() {
		var record WorkflowDefinitionRecord
		var definition string
		if err := rows.Scan(&record.ID, &definition, &record.DefinitionDigest, &record.Status,
			&record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		record.Definition = json.RawMessage(definition)
		records = append(records, &record)
	}
	return records, rows.Err()
}

func (s *SQLite) SaveWorkflowNode(record *WorkflowNodeRecord) error {
	_, err := s.db.Exec(`INSERT INTO workflow_nodes(run_id, node_id, attempt, input_hash, status, result, error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(run_id, node_id, attempt) DO UPDATE SET
		status=excluded.status, result=excluded.result, error=excluded.error, updated_at=excluded.updated_at`,
		record.RunID, record.NodeID, record.Attempt, record.InputHash, record.Status,
		string(record.Result), record.Error, time.Now())
	return err
}

func (s *SQLite) WorkflowNodeRecords(runID string) ([]*WorkflowNodeRecord, error) {
	rows, err := s.db.Query(`SELECT run_id, node_id, attempt, input_hash, status, result, error, updated_at
		FROM workflow_nodes WHERE run_id = ? ORDER BY node_id, attempt`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*WorkflowNodeRecord
	for rows.Next() {
		var record WorkflowNodeRecord
		var result, errorText sql.NullString
		if err := rows.Scan(&record.RunID, &record.NodeID, &record.Attempt, &record.InputHash,
			&record.Status, &result, &errorText, &record.UpdatedAt); err != nil {
			return nil, err
		}
		record.Result = json.RawMessage(result.String)
		record.Error = errorText.String
		records = append(records, &record)
	}
	return records, rows.Err()
}
