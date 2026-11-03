package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type schemaMigration struct {
	version int
	sql     []string
}

var schemaMigrations = []schemaMigration{
	{
		version: 1,
		sql: []string{
			`CREATE TABLE IF NOT EXISTS conversations (
				id TEXT PRIMARY KEY, title TEXT, source TEXT, metadata JSON,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				archived_at DATETIME NULL
			)`,
			`CREATE TABLE IF NOT EXISTS conversation_logs (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				conversation_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT,
				intent TEXT, actions JSON, prompt_tokens INTEGER DEFAULT 0,
				completion_tokens INTEGER DEFAULT 0, context_tokens INTEGER DEFAULT 0,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
			`CREATE TABLE IF NOT EXISTS agent_memories (
				id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL,
				category TEXT DEFAULT 'fact', source TEXT DEFAULT 'auto_observed',
				importance REAL DEFAULT 0.5, enabled BOOLEAN DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS mcp_servers (
				id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL,
				transport TEXT NOT NULL, config JSON NOT NULL, enabled BOOLEAN DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE TABLE IF NOT EXISTS vector_store (
				id INTEGER PRIMARY KEY AUTOINCREMENT, source_type TEXT NOT NULL,
				source_id TEXT NOT NULL, embedding BLOB, content TEXT,
				content_summary TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_conversation_logs_conv_id ON conversation_logs(conversation_id)`,
			`CREATE INDEX IF NOT EXISTS idx_conversation_logs_created ON conversation_logs(created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_memories_category ON agent_memories(category)`,
			`CREATE INDEX IF NOT EXISTS idx_agent_memories_enabled ON agent_memories(enabled)`,
			`CREATE INDEX IF NOT EXISTS idx_vector_store_source ON vector_store(source_type, source_id)`,
			`CREATE TABLE IF NOT EXISTS checkpoints (
				conversation_id TEXT PRIMARY KEY, data JSON NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
		},
	},
	{
		version: 2,
		sql: []string{
			`ALTER TABLE conversation_logs ADD COLUMN run_id TEXT`,
			`ALTER TABLE conversation_logs ADD COLUMN tool_calls JSON`,
			`ALTER TABLE conversation_logs ADD COLUMN tool_call_id TEXT`,
			`ALTER TABLE conversation_logs ADD COLUMN status TEXT`,
			`CREATE TABLE IF NOT EXISTS runs (
				id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, status TEXT NOT NULL,
				input TEXT NOT NULL, source TEXT, metadata JSON, iteration INTEGER DEFAULT 0,
				reply TEXT, error TEXT, usage JSON,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, finished_at DATETIME NULL,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_runs_conversation ON runs(conversation_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status)`,
			`CREATE TABLE IF NOT EXISTS run_events (
				event_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
				sequence INTEGER NOT NULL, type TEXT NOT NULL, payload JSON NOT NULL,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(run_id, sequence),
				FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id, sequence)`,
			`CREATE TABLE IF NOT EXISTS conversation_summaries (
				conversation_id TEXT PRIMARY KEY, summary TEXT NOT NULL,
				through_log_id INTEGER NOT NULL DEFAULT 0, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
			)`,
			`CREATE TABLE IF NOT EXISTS host_callbacks (
				name TEXT PRIMARY KEY, description TEXT NOT NULL, schema JSON NOT NULL,
				callback_url TEXT NOT NULL, secret TEXT, enabled BOOLEAN DEFAULT 1,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			)`,
		},
	},
	{
		version: 3,
		sql: []string{
			`ALTER TABLE host_callbacks ADD COLUMN allow_private BOOLEAN DEFAULT 0`,
		},
	},
}

func migrationChecksum(m schemaMigration) string {
	sum := sha256.Sum256([]byte(strings.Join(m.sql, ";\n")))
	return hex.EncodeToString(sum[:])
}

func (s *SQLite) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at DATETIME NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	applied := make(map[int]string)
	rows, err := s.db.Query(`SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		applied[version] = checksum
	}
	if err := rows.Close(); err != nil {
		return err
	}

	pending := 0
	for _, migration := range schemaMigrations {
		checksum := migrationChecksum(migration)
		if previous, ok := applied[migration.version]; ok {
			if previous != checksum {
				return fmt.Errorf("migration %d checksum mismatch; historical migrations are immutable", migration.version)
			}
			continue
		}
		pending++
	}

	if pending > 0 && s.dbPath != ":memory:" {
		hasData, err := s.hasExistingData()
		if err != nil {
			return err
		}
		if hasData {
			if err := s.backupForMigration(); err != nil {
				return fmt.Errorf("backup before migration: %w", err)
			}
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	for _, migration := range schemaMigrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		for _, statement := range migration.sql {
			if err := execMigrationStatement(tx, statement); err != nil {
				return fmt.Errorf("migration %d: %w", migration.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?, ?, ?)`,
			migration.version, migrationChecksum(migration), time.Now()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	s.initializeFTS()
	return nil
}

func execMigrationStatement(tx *sql.Tx, statement string) error {
	fields := strings.Fields(statement)
	if len(fields) >= 6 && strings.EqualFold(fields[0], "ALTER") && strings.EqualFold(fields[3], "ADD") {
		table, column := fields[2], fields[5]
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	_, err := tx.Exec(statement)
	return err
}

func (s *SQLite) hasExistingData() (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name NOT IN ('schema_migrations', 'sqlite_sequence')`).Scan(&count)
	return count > 0, err
}

func (s *SQLite) backupForMigration() error {
	backupDir := filepath.Join(filepath.Dir(s.dbPath), "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	backupPath := filepath.Join(backupDir, fmt.Sprintf("agent-pre-migration-%s.db", time.Now().UTC().Format("20060102T150405.000000000Z")))
	if _, err := s.db.Exec(`VACUUM INTO ?`, backupPath); err != nil {
		return err
	}
	s.lastBackup = backupPath
	return nil
}

func (s *SQLite) initializeFTS() {
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS conversation_logs_fts USING fts5(
			conversation_id, content, content='conversation_logs', content_rowid='id'
		)`,
		`CREATE TRIGGER IF NOT EXISTS conversation_logs_ai AFTER INSERT ON conversation_logs BEGIN
			INSERT INTO conversation_logs_fts(rowid, conversation_id, content) VALUES (new.id, new.conversation_id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS conversation_logs_ad AFTER DELETE ON conversation_logs BEGIN
			INSERT INTO conversation_logs_fts(conversation_logs_fts, rowid, conversation_id, content)
			VALUES ('delete', old.id, old.conversation_id, old.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS conversation_logs_au AFTER UPDATE ON conversation_logs BEGIN
			INSERT INTO conversation_logs_fts(conversation_logs_fts, rowid, conversation_id, content)
			VALUES ('delete', old.id, old.conversation_id, old.content);
			INSERT INTO conversation_logs_fts(rowid, conversation_id, content) VALUES (new.id, new.conversation_id, new.content);
		END`,
		`INSERT INTO conversation_logs_fts(rowid, conversation_id, content)
			SELECT id, conversation_id, content FROM conversation_logs
			WHERE id NOT IN (SELECT rowid FROM conversation_logs_fts)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			s.ftsEnabled = false
			return
		}
	}
	s.ftsEnabled = true
}

// LastMigrationBackup returns the backup created during startup, if any.
func (s *SQLite) LastMigrationBackup() string { return s.lastBackup }

// FTSAvailable reports whether conversation full-text search is usable.
func (s *SQLite) FTSAvailable() bool { return s.ftsEnabled }
