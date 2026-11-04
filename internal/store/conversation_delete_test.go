package store

import (
	"agent-core/internal/model"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

var conversationDeleteTables = []struct {
	table  string
	column string
}{
	{"conversations", "id"},
	{"conversation_logs", "conversation_id"},
	{"runs", "conversation_id"},
	{"run_events", "conversation_id"},
	{"run_checkpoints", "conversation_id"},
	{"checkpoints", "conversation_id"},
	{"conversation_summaries", "conversation_id"},
	{"accepted_commands", "conversation_id"},
	{"workflow_runs", "id"},
	{"workflow_nodes", "run_id"},
}

func seedConversationDeletionGraph(t *testing.T, s *SQLite, id string) {
	t.Helper()
	createTestConversation(t, s, id)
	now := time.Now()
	if err := s.AddLog(&model.ConversationLog{ConversationID: id, Role: model.RoleUser,
		Content: "permanentdeletemarker " + id, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runID := id + "-run"
	finished := now
	if err := s.SaveRun(&model.Run{ID: runID, ConversationID: id, Status: model.RunStatusCompleted,
		Input: "input", CreatedAt: now, UpdatedAt: now, FinishedAt: &finished, Usage: &model.UsageInfo{}}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"content": "event"})
	if err := s.AppendRunEvent(&model.RunEvent{EventID: id + "-event", RunID: runID, ConversationID: id,
		Sequence: 1, Type: "chunk", Payload: payload, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO run_checkpoints(run_id,conversation_id,data,created_at) VALUES(?,?,?,?)`, []any{runID, id, `{}`, now}},
		{`INSERT INTO checkpoints(conversation_id,data,created_at) VALUES(?,?,?)`, []any{id, `{}`, now}},
		{`INSERT INTO conversation_summaries(conversation_id,summary,through_log_id,updated_at) VALUES(?,?,?,?)`, []any{id, "summary", 0, now}},
		{`INSERT INTO accepted_commands(command_id,fingerprint,run_id,conversation_id) VALUES(?,?,?,?)`, []any{id + "-command", "fingerprint", runID, id}},
		{`INSERT INTO workflow_runs(id,definition,definition_digest,status,created_at,updated_at) VALUES(?,?,?,?,?,?)`, []any{id, `{}`, "digest", "completed", now, now}},
		{`INSERT INTO workflow_nodes(run_id,node_id,attempt,input_hash,status,updated_at) VALUES(?,?,?,?,?,?)`, []any{id, "node", 1, "hash", "completed", now}},
	}
	for _, statement := range statements {
		if _, err := s.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", statement.query, err)
		}
	}
}

func assertConversationRowCount(t *testing.T, s *SQLite, table, column, id string, want int) {
	t.Helper()
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, table, column)
	var count int
	if err := s.DB().QueryRow(query, id).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s count for %s = %d, want %d", table, id, count, want)
	}
}

func assertForeignKeysValid(t *testing.T, s *SQLite) {
	t.Helper()
	rows, err := s.DB().Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after conversation deletion")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func assertConversationFTSContentCount(t *testing.T, s *SQLite, content string, want int) {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM conversation_logs_fts WHERE content MATCH ?`, content).Scan(&count); err != nil {
		t.Fatalf("search conversation FTS: %v", err)
	}
	if count != want {
		t.Fatalf("FTS content count = %d, want %d", count, want)
	}
}

func TestPermanentlyDeleteConversationRemovesRelatedData(t *testing.T) {
	s := newTestStore(t)
	seedConversationDeletionGraph(t, s, "target")
	seedConversationDeletionGraph(t, s, "other")
	if s.FTSAvailable() {
		assertConversationFTSContentCount(t, s, "permanentdeletemarker", 2)
	}
	if err := s.ArchiveConversation("target"); err != nil {
		t.Fatal(err)
	}
	if err := s.PermanentlyDeleteConversation("target"); err != nil {
		t.Fatal(err)
	}
	for _, table := range conversationDeleteTables {
		assertConversationRowCount(t, s, table.table, table.column, "target", 0)
		assertConversationRowCount(t, s, table.table, table.column, "other", 1)
	}
	if s.FTSAvailable() {
		assertConversationFTSContentCount(t, s, "permanentdeletemarker", 1)
	}
	conversation, err := s.GetConversation("target")
	if err != nil || conversation != nil {
		t.Fatalf("deleted conversation remains available: %#v, %v", conversation, err)
	}
	assertForeignKeysValid(t, s)
}

func TestPermanentlyDeleteConversationRejectsActiveRuns(t *testing.T) {
	for _, status := range []model.RunStatus{
		model.RunStatusPending,
		model.RunStatusRunning,
		model.RunStatusWaitingClarify,
		model.RunStatusWaitingApproval,
	} {
		t.Run(string(status), func(t *testing.T) {
			s := newTestStore(t)
			createTestConversation(t, s, "target")
			now := time.Now()
			if err := s.AddLog(&model.ConversationLog{ConversationID: "target", Role: model.RoleUser, Content: "keep", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveRun(&model.Run{ID: "run", ConversationID: "target", Status: status,
				Input: "input", CreatedAt: now, UpdatedAt: now, Usage: &model.UsageInfo{}}); err != nil {
				t.Fatal(err)
			}
			err := s.PermanentlyDeleteConversation("target")
			if !errors.Is(err, ErrConversationActive) {
				t.Fatalf("got %v, want ErrConversationActive", err)
			}
			assertConversationRowCount(t, s, "conversations", "id", "target", 1)
			assertConversationRowCount(t, s, "conversation_logs", "conversation_id", "target", 1)
			assertConversationRowCount(t, s, "runs", "conversation_id", "target", 1)
		})
	}
}

func TestPermanentlyDeleteConversationRollsBack(t *testing.T) {
	s := newTestStore(t)
	seedConversationDeletionGraph(t, s, "target")
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_conversation_delete BEFORE DELETE ON conversations
		WHEN old.id = 'target' BEGIN SELECT RAISE(ABORT, 'forced deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.PermanentlyDeleteConversation("target"); err == nil {
		t.Fatal("expected forced deletion failure")
	}
	for _, table := range conversationDeleteTables {
		assertConversationRowCount(t, s, table.table, table.column, "target", 1)
	}
	assertForeignKeysValid(t, s)
}

func TestPermanentlyDeleteConversationNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.PermanentlyDeleteConversation("missing"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("got %v, want ErrConversationNotFound", err)
	}
}

func TestSaveWorkflowDoesNotRecreateDeletedConversation(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conversation")
	record := &WorkflowDefinitionRecord{ID: "conversation", Definition: json.RawMessage(`{}`),
		DefinitionDigest: "digest", Status: "running", CreatedAt: time.Now()}
	if err := s.SaveWorkflow(record); err != nil {
		t.Fatal(err)
	}
	record.Status = "completed"
	if err := s.SaveWorkflow(record); err != nil {
		t.Fatal(err)
	}
	assertConversationRowCount(t, s, "workflow_runs", "id", "conversation", 1)
	if err := s.PermanentlyDeleteConversation("conversation"); err != nil {
		t.Fatal(err)
	}
	record.Status = "running"
	if err := s.SaveWorkflow(record); err != nil {
		t.Fatal(err)
	}
	assertConversationRowCount(t, s, "workflow_runs", "id", "conversation", 0)
}
