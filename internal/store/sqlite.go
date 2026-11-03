package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agent-core/internal/model"

	_ "github.com/mattn/go-sqlite3"
)

// SQLite provides the SQLite-backed data store for Agent Core.
type SQLite struct {
	db         *sql.DB
	dbPath     string
	ftsEnabled bool
	lastBackup string
}

// NewSQLite creates a new SQLite store, initializing the database if needed.
func NewSQLite(dbPath string) (*SQLite, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite only supports one concurrent writer even in WAL mode.
	// Limit pool to 1 connection to serialize writes and prevent "database is locked" errors.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &SQLite{db: db, dbPath: dbPath}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return store, nil
}

// Close closes the database connection.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection for advanced queries.
func (s *SQLite) DB() *sql.DB {
	return s.db
}

// migrate applies versioned migrations in migrations.go.

// --- Conversation Operations ---

// CreateConversation creates a new conversation.
func (s *SQLite) CreateConversation(conv *model.Conversation) error {
	metadata, _ := json.Marshal(conv.Metadata)
	_, err := s.db.Exec(
		`INSERT INTO conversations (id, title, source, metadata, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		conv.ID, conv.Title, conv.Source, string(metadata), conv.CreatedAt, conv.UpdatedAt,
	)
	return err
}

// GetConversation retrieves a conversation by ID.
func (s *SQLite) GetConversation(id string) (*model.Conversation, error) {
	var conv model.Conversation
	var metadata sql.NullString
	var archivedAt sql.NullTime

	err := s.db.QueryRow(
		`SELECT id, title, source, metadata, created_at, updated_at, archived_at FROM conversations WHERE id = ?`, id,
	).Scan(&conv.ID, &conv.Title, &conv.Source, &metadata, &conv.CreatedAt, &conv.UpdatedAt, &archivedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if metadata.Valid && metadata.String != "" {
		json.Unmarshal([]byte(metadata.String), &conv.Metadata)
	}
	if archivedAt.Valid {
		conv.ArchivedAt = &archivedAt.Time
	}

	return &conv, nil
}

// ListConversations returns conversations, optionally filtered by source.
func (s *SQLite) ListConversations(source string, limit, offset int) ([]*model.Conversation, error) {
	query := `SELECT id, title, source, metadata, created_at, updated_at, archived_at 
		FROM conversations WHERE archived_at IS NULL`
	args := []any{}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}

	query += " ORDER BY updated_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conversations []*model.Conversation
	for rows.Next() {
		var conv model.Conversation
		var metadata sql.NullString
		var archivedAt sql.NullTime

		if err := rows.Scan(&conv.ID, &conv.Title, &conv.Source, &metadata, &conv.CreatedAt, &conv.UpdatedAt, &archivedAt); err != nil {
			return nil, err
		}

		if metadata.Valid && metadata.String != "" {
			json.Unmarshal([]byte(metadata.String), &conv.Metadata)
		}
		if archivedAt.Valid {
			conv.ArchivedAt = &archivedAt.Time
		}

		conversations = append(conversations, &conv)
	}

	return conversations, rows.Err()
}

// UpdateConversationTitle updates the title of a conversation.
func (s *SQLite) UpdateConversationTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`, title, time.Now(), id)
	return err
}

// ArchiveConversation soft-deletes a conversation.
func (s *SQLite) ArchiveConversation(id string) error {
	now := time.Now()
	_, err := s.db.Exec(`UPDATE conversations SET archived_at = ?, updated_at = ? WHERE id = ?`, now, now, id)
	return err
}

// --- Conversation Log Operations ---

// AddLog adds a log entry and advances the conversation timestamp atomically.
func (s *SQLite) AddLog(log *model.ConversationLog) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addLogTx(tx, log); err != nil {
		return err
	}
	return tx.Commit()
}

func addLogTx(tx *sql.Tx, log *model.ConversationLog) error {
	actions, err := json.Marshal(log.Actions)
	if err != nil {
		return err
	}
	toolCalls, err := json.Marshal(log.ToolCalls)
	if err != nil {
		return err
	}
	result, err := tx.Exec(
		`INSERT INTO conversation_logs (
			conversation_id, run_id, role, content, tool_calls, tool_call_id, status,
			intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ConversationID, log.RunID, log.Role, log.Content, string(toolCalls), log.ToolCallID,
		log.Status, log.Intent, string(actions), log.PromptTokens, log.CompletionTokens,
		log.ContextTokens, log.CreatedAt,
	)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE conversations SET updated_at = ? WHERE id = ?`, time.Now(), log.ConversationID); err != nil {
		return err
	}
	log.ID, err = result.LastInsertId()
	return err
}

// CommitToolResult atomically records a tool result and its resumable boundary.
func (s *SQLite) CommitToolResult(log *model.ConversationLog, checkpoint []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addLogTx(tx, log); err != nil {
		return err
	}
	now := time.Now()
	if log.RunID != "" {
		if _, err := tx.Exec(`INSERT INTO run_checkpoints(run_id, conversation_id, data, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(run_id) DO UPDATE SET data=excluded.data, created_at=excluded.created_at`,
			log.RunID, log.ConversationID, checkpoint, now); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`INSERT INTO checkpoints(conversation_id, data, created_at) VALUES (?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET data=excluded.data, created_at=excluded.created_at`,
		log.ConversationID, checkpoint, now); err != nil {
		return err
	}
	return tx.Commit()
}

// GetLogs retrieves logs for a conversation.
func (s *SQLite) GetLogs(conversationID string, limit int) ([]*model.ConversationLog, error) {
	rows, err := s.db.Query(
		`SELECT id, conversation_id, run_id, role, content, tool_calls, tool_call_id, status,
			intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at
		FROM (
			SELECT id, conversation_id, run_id, role, content, tool_calls, tool_call_id, status,
				intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at
			FROM conversation_logs WHERE conversation_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*model.ConversationLog
	for rows.Next() {
		var cl model.ConversationLog
		var runID, toolCalls, toolCallID, status, actions sql.NullString

		if err := rows.Scan(&cl.ID, &cl.ConversationID, &runID, &cl.Role, &cl.Content,
			&toolCalls, &toolCallID, &status, &cl.Intent, &actions, &cl.PromptTokens,
			&cl.CompletionTokens, &cl.ContextTokens, &cl.CreatedAt); err != nil {
			return nil, err
		}
		cl.RunID = runID.String
		cl.ToolCallID = toolCallID.String
		cl.Status = status.String
		if toolCalls.Valid && toolCalls.String != "" {
			if err := json.Unmarshal([]byte(toolCalls.String), &cl.ToolCalls); err != nil {
				return nil, fmt.Errorf("decode tool calls for log %d: %w", cl.ID, err)
			}
		}
		if actions.Valid && actions.String != "" {
			if err := json.Unmarshal([]byte(actions.String), &cl.Actions); err != nil {
				return nil, fmt.Errorf("decode actions for log %d: %w", cl.ID, err)
			}
		}
		logs = append(logs, &cl)
	}

	return logs, rows.Err()
}

// CountLogTokens returns the total tokens in a conversation's logs.
func (s *SQLite) CountLogTokens(conversationID string) (int, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(context_tokens), 0) FROM conversation_logs WHERE conversation_id = ?`,
		conversationID,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return int(total.Int64), nil
}

// --- Memory Operations ---

// AddMemory creates a new memory entry.
func (s *SQLite) AddMemory(mem *model.Memory) error {
	result, err := s.db.Exec(
		`INSERT INTO agent_memories (content, category, source, importance, enabled, created_at, updated_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mem.Content, mem.Category, mem.Source, mem.Importance, mem.Enabled, time.Now(), time.Now(),
	)
	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	mem.ID = id
	return nil
}

// GetMemories retrieves memories with optional filtering.
func (s *SQLite) GetMemories(category string, enabled *bool, limit int) ([]*model.Memory, error) {
	query := `SELECT id, content, category, source, importance, enabled, created_at, updated_at FROM agent_memories WHERE 1=1`
	args := []any{}

	if category != "" {
		query += " AND category = ?"
		args = append(args, category)
	}
	if enabled != nil {
		query += " AND enabled = ?"
		args = append(args, *enabled)
	}

	query += " ORDER BY importance DESC, updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []*model.Memory
	for rows.Next() {
		var mem model.Memory
		if err := rows.Scan(&mem.ID, &mem.Content, &mem.Category, &mem.Source, &mem.Importance,
			&mem.Enabled, &mem.CreatedAt, &mem.UpdatedAt); err != nil {
			return nil, err
		}
		memories = append(memories, &mem)
	}

	return memories, rows.Err()
}

// GetMemory retrieves a single memory by ID.
func (s *SQLite) GetMemory(id int64) (*model.Memory, error) {
	var mem model.Memory
	err := s.db.QueryRow(
		`SELECT id, content, category, source, importance, enabled, created_at, updated_at FROM agent_memories WHERE id = ?`, id,
	).Scan(&mem.ID, &mem.Content, &mem.Category, &mem.Source, &mem.Importance, &mem.Enabled, &mem.CreatedAt, &mem.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mem, nil
}

// UpdateMemory updates an existing memory.
func (s *SQLite) UpdateMemory(mem *model.Memory) error {
	_, err := s.db.Exec(
		`UPDATE agent_memories SET content = ?, category = ?, source = ?, importance = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		mem.Content, mem.Category, mem.Source, mem.Importance, mem.Enabled, time.Now(), mem.ID,
	)
	return err
}

// DeleteMemory removes a memory by ID.
func (s *SQLite) DeleteMemory(id int64) error {
	_, err := s.db.Exec(`DELETE FROM agent_memories WHERE id = ?`, id)
	return err
}

// CountMemories returns the number of enabled memories.
func (s *SQLite) CountMemories() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_memories WHERE enabled = 1`).Scan(&count)
	return count, err
}

// --- MCP Server Operations ---

// SaveMCPServer saves an MCP server configuration.
func (s *SQLite) SaveMCPServer(server *model.MCPServer) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO mcp_servers (name, transport, config, enabled) VALUES (?, ?, ?, ?)`,
		server.Name, server.Transport, server.Config, server.Enabled,
	)
	return err
}

// GetMCPServers retrieves all MCP server configurations.
func (s *SQLite) GetMCPServers() ([]*model.MCPServer, error) {
	rows, err := s.db.Query(`SELECT id, name, transport, config, enabled FROM mcp_servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []*model.MCPServer
	for rows.Next() {
		var srv model.MCPServer
		if err := rows.Scan(&srv.ID, &srv.Name, &srv.Transport, &srv.Config, &srv.Enabled); err != nil {
			return nil, err
		}
		servers = append(servers, &srv)
	}

	return servers, rows.Err()
}

// DeleteMCPServer removes an MCP server configuration.
func (s *SQLite) DeleteMCPServer(name string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_servers WHERE name = ?`, name)
	return err
}

// --- Checkpoint Operations ---

// SaveCheckpointData saves a run-scoped checkpoint.
func (s *SQLite) SaveCheckpointData(runID, conversationID string, data []byte) error {
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO run_checkpoints(run_id, conversation_id, data, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET data=excluded.data, created_at=excluded.created_at`,
		runID, conversationID, data, now)
	return err
}

// LoadCheckpointData loads a checkpoint by exact run ID.
func (s *SQLite) LoadCheckpointData(runID string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRow(`SELECT data FROM run_checkpoints WHERE run_id = ?`, runID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return data, err
}

// LoadLegacyCheckpointData reads the pre-v4 conversation-scoped format.
func (s *SQLite) LoadLegacyCheckpointData(conversationID string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRow(`SELECT data FROM checkpoints WHERE conversation_id = ?`, conversationID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return data, err
}

// DeleteCheckpointData deletes only the specified run's checkpoint.
func (s *SQLite) DeleteCheckpointData(runID string) error {
	_, err := s.db.Exec(`DELETE FROM run_checkpoints WHERE run_id = ?`, runID)
	return err
}
