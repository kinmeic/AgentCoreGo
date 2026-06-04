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
	db *sql.DB
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

	store := &SQLite{db: db}
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

// migrate runs database migrations.
func (s *SQLite) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			title TEXT,
			source TEXT,
			metadata JSON,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			archived_at DATETIME NULL
		)`,
		`CREATE TABLE IF NOT EXISTS conversation_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conversation_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT,
			intent TEXT,
			actions JSON,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			context_tokens INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			category TEXT DEFAULT 'fact',
			source TEXT DEFAULT 'auto_observed',
			importance REAL DEFAULT 0.5,
			enabled BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			transport TEXT NOT NULL,
			config JSON NOT NULL,
			enabled BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS vector_store (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			embedding BLOB,
			content TEXT,
			content_summary TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_logs_conv_id ON conversation_logs(conversation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_logs_created ON conversation_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_memories_category ON agent_memories(category)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_memories_enabled ON agent_memories(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_vector_store_source ON vector_store(source_type, source_id)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}
	}

	// Try to create FTS5 table (may not be available in all SQLite builds)
	_, _ = s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS conversation_logs_fts USING fts5(
		conversation_id,
		content,
		content='conversation_logs',
		content_rowid='id'
	)`)

	return nil
}

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

// AddLog adds a log entry to a conversation.
func (s *SQLite) AddLog(log *model.ConversationLog) error {
	actions, _ := json.Marshal(log.Actions)
	result, err := s.db.Exec(
		`INSERT INTO conversation_logs (conversation_id, role, content, intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ConversationID, log.Role, log.Content, log.Intent, string(actions),
		log.PromptTokens, log.CompletionTokens, log.ContextTokens, log.CreatedAt,
	)
	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	log.ID = id

	// Update conversation timestamp
	s.db.Exec(`UPDATE conversations SET updated_at = ? WHERE id = ?`, time.Now(), log.ConversationID)

	return nil
}

// GetLogs retrieves logs for a conversation.
func (s *SQLite) GetLogs(conversationID string, limit int) ([]*model.ConversationLog, error) {
	rows, err := s.db.Query(
		`SELECT id, conversation_id, role, content, intent, actions, prompt_tokens, completion_tokens, context_tokens, created_at 
		FROM conversation_logs WHERE conversation_id = ? ORDER BY id ASC LIMIT ?`,
		conversationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*model.ConversationLog
	for rows.Next() {
		var cl model.ConversationLog
		var actions sql.NullString

		if err := rows.Scan(&cl.ID, &cl.ConversationID, &cl.Role, &cl.Content, &cl.Intent,
			&actions, &cl.PromptTokens, &cl.CompletionTokens, &cl.ContextTokens, &cl.CreatedAt); err != nil {
			return nil, err
		}

		if actions.Valid && actions.String != "" {
			json.Unmarshal([]byte(actions.String), &cl.Actions)
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
