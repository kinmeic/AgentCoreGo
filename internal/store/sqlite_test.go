package store

import (
	"agent-core/internal/model"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	s, err := NewSQLite(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func createTestConversation(t *testing.T, s *SQLite, id string) {
	t.Helper()
	now := time.Now()
	if err := s.CreateConversation(&model.Conversation{ID: id, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
}

func TestGetLogsReturnsLatestEntriesInChronologicalOrder(t *testing.T) {
	s := newTestStore(t)
	createTestConversation(t, s, "conv")
	for i := 1; i <= 105; i++ {
		if err := s.AddLog(&model.ConversationLog{
			ConversationID: "conv",
			Role:           model.RoleUser,
			Content:        fmt.Sprintf("message-%03d", i),
			CreatedAt:      time.Now(),
		}); err != nil {
			t.Fatalf("add log %d: %v", i, err)
		}
	}

	logs, err := s.GetLogs("conv", 100)
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	if len(logs) != 100 {
		t.Fatalf("got %d logs, want 100", len(logs))
	}
	if logs[0].Content != "message-006" || logs[99].Content != "message-105" {
		t.Fatalf("unexpected range: first=%q last=%q", logs[0].Content, logs[99].Content)
	}
	for i := 1; i < len(logs); i++ {
		if logs[i-1].ID >= logs[i].ID {
			t.Fatalf("logs are not chronological at %d", i)
		}
	}
}
