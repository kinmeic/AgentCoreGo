package store

import (
	"agent-core/internal/model"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunAndEventRoundTrip(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conv")
	run := &model.Run{ID: "run", ConversationID: "conv", Status: model.RunStatusRunning,
		Input: "hello", CreatedAt: time.Now(), Usage: &model.UsageInfo{TotalTokens: 3}}
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("save run: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"content": "chunk"})
	event := &model.RunEvent{EventID: "event", RunID: "run", ConversationID: "conv",
		Sequence: 1, Type: "chunk", Payload: payload, CreatedAt: time.Now()}
	if err := s.AppendRunEvent(event); err != nil {
		t.Fatalf("append event: %v", err)
	}
	run.Status = model.RunStatusCompleted
	now := time.Now()
	run.FinishedAt = &now
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	got, err := s.GetRun("run")
	if err != nil || got == nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != model.RunStatusCompleted || got.Usage.TotalTokens != 3 || got.FinishedAt == nil {
		t.Fatalf("unexpected run: %#v", got)
	}
	events, err := s.RunEvents("run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "chunk" {
		t.Fatalf("unexpected events: %#v, %v", events, err)
	}
}

func TestLogCursorAndSearch(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conv")
	for _, content := range []string{"alpha one", "beta two", "alpha three"} {
		if err := s.AddLog(&model.ConversationLog{ConversationID: "conv", Role: model.RoleUser,
			Content: content, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.GetLogsBefore("conv", 3, 1)
	if err != nil || len(page) != 1 || page[0].Content != "beta two" {
		t.Fatalf("unexpected cursor page: %#v, %v", page, err)
	}
	matches, err := s.SearchLogs("conv", "alpha", 10)
	if err != nil || len(matches) != 2 {
		t.Fatalf("unexpected search results: %#v, %v", matches, err)
	}
}

func TestLegacyMigrationCreatesBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE conversations (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	defer s.Close()
	if s.LastMigrationBackup() == "" {
		t.Fatal("expected a migration backup")
	}
	if _, err := os.Stat(s.LastMigrationBackup()); err != nil {
		t.Fatalf("backup is unavailable: %v", err)
	}
	var version int
	if err := s.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("unexpected schema version %d: %v", version, err)
	}
}
