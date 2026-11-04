package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"agent-core/internal/model"
)

// SaveRun inserts or updates a durable run record.
func (s *SQLite) SaveRun(run *model.Run) error {
	metadata, err := json.Marshal(run.Metadata)
	if err != nil {
		return err
	}
	usage, err := json.Marshal(run.Usage)
	if err != nil {
		return err
	}
	spec, err := json.Marshal(run.Spec)
	if err != nil {
		return err
	}
	run.UpdatedAt = time.Now()
	_, err = s.db.Exec(`INSERT INTO runs (
		id, conversation_id, status, input, source, metadata, iteration, reply, error, usage,
		created_at, updated_at, finished_at, parent_run_id, root_run_id, depth, kind, spec
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
 spec=CASE WHEN runs.status='pending' THEN excluded.spec ELSE runs.spec END,
 parent_run_id=CASE WHEN runs.status='pending' THEN excluded.parent_run_id ELSE runs.parent_run_id END,
 root_run_id=CASE WHEN runs.status='pending' THEN excluded.root_run_id ELSE runs.root_run_id END,
 depth=CASE WHEN runs.status='pending' THEN excluded.depth ELSE runs.depth END,
 kind=CASE WHEN runs.status='pending' THEN excluded.kind ELSE runs.kind END,
 metadata=CASE WHEN runs.status='pending' THEN excluded.metadata ELSE runs.metadata END,
 status=excluded.status, iteration=excluded.iteration,
		reply=excluded.reply, error=excluded.error, usage=excluded.usage,
		updated_at=excluded.updated_at, finished_at=excluded.finished_at`,
		run.ID, run.ConversationID, run.Status, run.Input, run.Source, string(metadata),
		run.Iteration, run.Reply, run.Error, string(usage), run.CreatedAt, run.UpdatedAt, run.FinishedAt,
		run.Spec.ParentRunID, run.Spec.RootRunID, run.Spec.Depth, run.Spec.Kind, string(spec))
	return err
}

// GetRun returns a run by ID, or nil when it does not exist.
func (s *SQLite) GetRun(id string) (*model.Run, error) {
	row := s.db.QueryRow(`SELECT id, conversation_id, status, input, source, metadata,
		iteration, reply, error, usage, created_at, updated_at, finished_at,
		parent_run_id, root_run_id, depth, kind, spec FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

// LatestRun returns the newest run for a conversation.
func (s *SQLite) LatestRun(conversationID string) (*model.Run, error) {
	row := s.db.QueryRow(`SELECT id, conversation_id, status, input, source, metadata,
		iteration, reply, error, usage, created_at, updated_at, finished_at,
		parent_run_id, root_run_id, depth, kind, spec
		FROM runs WHERE conversation_id = ? ORDER BY created_at DESC LIMIT 1`, conversationID)
	return scanRun(row)
}

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (*model.Run, error) {
	var run model.Run
	var metadata, usage, spec sql.NullString
	var parentRunID, rootRunID, kind sql.NullString
	var finishedAt sql.NullTime
	if err := row.Scan(&run.ID, &run.ConversationID, &run.Status, &run.Input, &run.Source,
		&metadata, &run.Iteration, &run.Reply, &run.Error, &usage, &run.CreatedAt,
		&run.UpdatedAt, &finishedAt, &parentRunID, &rootRunID, &run.Spec.Depth, &kind, &spec); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	run.Spec.ParentRunID = parentRunID.String
	run.Spec.RootRunID = rootRunID.String
	run.Spec.Kind = model.RunKind(kind.String)
	if run.Spec.Kind == "" {
		run.Spec.Kind = model.RunKindRoot
	}
	if spec.Valid && spec.String != "" && spec.String != "null" {
		if err := json.Unmarshal([]byte(spec.String), &run.Spec); err != nil {
			return nil, err
		}
	}
	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &run.Metadata); err != nil {
			return nil, err
		}
	}
	if usage.Valid && usage.String != "" && usage.String != "null" {
		if err := json.Unmarshal([]byte(usage.String), &run.Usage); err != nil {
			return nil, err
		}
	}
	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}
	return &run, nil
}

// AppendRunEvent persists an event in sequence order for replay and diagnostics.
func (s *SQLite) AppendRunEvent(event *model.RunEvent) error {
	_, err := s.db.Exec(`INSERT INTO run_events(
		event_id, run_id, conversation_id, sequence, type, payload, created_at,
		root_run_id, parent_run_id, trace_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.EventID, event.RunID, event.ConversationID,
		event.Sequence, event.Type, string(event.Payload), event.CreatedAt,
		event.RootRunID, event.ParentRunID, event.TraceID)
	return err
}

// RunEvents returns events after a sequence number in deterministic order.
func (s *SQLite) RunEvents(runID string, after uint64, limit int) ([]*model.RunEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT event_id, run_id, conversation_id, sequence, type, payload, created_at,
		root_run_id, parent_run_id, trace_id
		FROM run_events WHERE run_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT ?`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*model.RunEvent
	for rows.Next() {
		var event model.RunEvent
		var payload string
		var rootRunID, parentRunID, traceID sql.NullString
		if err := rows.Scan(&event.EventID, &event.RunID, &event.ConversationID, &event.Sequence,
			&event.Type, &payload, &event.CreatedAt, &rootRunID, &parentRunID, &traceID); err != nil {
			return nil, err
		}
		event.RootRunID = rootRunID.String
		event.ParentRunID = parentRunID.String
		event.TraceID = traceID.String
		event.Payload = json.RawMessage(payload)
		events = append(events, &event)
	}
	return events, rows.Err()
}

// LastRunEventSequence returns the durable replay cursor for a run.
func (s *SQLite) LastRunEventSequence(runID string) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM run_events WHERE run_id = ?`, runID).Scan(&sequence)
	return sequence, err
}

// GetLogsBefore returns a cursor-paginated slice in chronological order.
func (s *SQLite) GetLogsBefore(conversationID string, beforeID int64, limit int) ([]*model.ConversationLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = int64(^uint64(0) >> 1)
	}
	rows, err := s.db.Query(`SELECT id, conversation_id, run_id, role, content, tool_calls,
		tool_call_id, status, intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at
		FROM (SELECT * FROM conversation_logs WHERE conversation_id = ? AND id < ?
		ORDER BY id DESC LIMIT ?) ORDER BY id ASC`, conversationID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return scanLogs(rows)
}

// SearchLogs searches full text when FTS5 is available, otherwise uses LIKE.
func (s *SQLite) SearchLogs(conversationID, query string, limit int) ([]*model.ConversationLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	match, args := "l.content LIKE ?", []any{conversationID, "%" + query + "%"}
	from := "conversation_logs AS l"
	where := "l.conversation_id = ?"
	if s.ftsEnabled {
		from = "conversation_logs_fts AS f JOIN conversation_logs AS l ON l.id = f.rowid"
		match = "f MATCH ?"
		where = "f.conversation_id = ?"
		args = []any{conversationID, query}
	}
	rows, err := s.db.Query(`SELECT l.id, l.conversation_id, l.run_id, l.role, l.content,
		l.tool_calls, l.tool_call_id, l.status, l.intent, l.actions, l.prompt_tokens,
		l.completion_tokens, l.context_tokens, l.created_at FROM `+from+
		` WHERE `+where+` AND `+match+` ORDER BY l.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return scanLogs(rows)
}

func scanLogs(rows *sql.Rows) ([]*model.ConversationLog, error) {
	defer rows.Close()
	var logs []*model.ConversationLog
	for rows.Next() {
		var log model.ConversationLog
		var runID, toolCalls, toolCallID, status, actions sql.NullString
		if err := rows.Scan(&log.ID, &log.ConversationID, &runID, &log.Role, &log.Content,
			&toolCalls, &toolCallID, &status, &log.Intent, &actions, &log.PromptTokens,
			&log.CompletionTokens, &log.ContextTokens, &log.CreatedAt); err != nil {
			return nil, err
		}
		log.RunID, log.ToolCallID, log.Status = runID.String, toolCallID.String, status.String
		if toolCalls.Valid && toolCalls.String != "" {
			if err := json.Unmarshal([]byte(toolCalls.String), &log.ToolCalls); err != nil {
				return nil, fmt.Errorf("decode tool calls for log %d: %w", log.ID, err)
			}
		}
		if actions.Valid && actions.String != "" {
			if err := json.Unmarshal([]byte(actions.String), &log.Actions); err != nil {
				return nil, fmt.Errorf("decode actions for log %d: %w", log.ID, err)
			}
		}
		logs = append(logs, &log)
	}
	return logs, rows.Err()
}

// SaveConversationSummary persists compressed history through a log boundary.
func (s *SQLite) SaveConversationSummary(conversationID, summary string, throughLogID int64) error {
	_, err := s.db.Exec(`INSERT INTO conversation_summaries(conversation_id, summary, through_log_id, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET summary=excluded.summary,
		through_log_id=excluded.through_log_id, updated_at=excluded.updated_at`,
		conversationID, summary, throughLogID, time.Now())
	return err
}

// ConversationSummary returns the latest durable summary.
func (s *SQLite) ConversationSummary(conversationID string) (string, int64, error) {
	var summary string
	var through int64
	err := s.db.QueryRow(`SELECT summary, through_log_id FROM conversation_summaries WHERE conversation_id = ?`, conversationID).Scan(&summary, &through)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	return summary, through, err
}

// Ping verifies that the database can execute a query.
func (s *SQLite) Ping() error {
	var one int
	return s.db.QueryRow(`SELECT 1`).Scan(&one)
}
