package subagents

import (
	"agent-core/internal/model"
	"agent-core/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestExploreUsesCurrentParentMode(t *testing.T) {
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "subagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if err := db.CreateConversation(&model.Conversation{ID: "conv", Metadata: map[string]string{"mode": "build"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	parent := &model.Run{ID: "parent", ConversationID: "conv", Status: model.RunStatusCompleted,
		Input: "test", CreatedAt: now, Spec: model.RunSpec{RootRunID: "parent", Kind: model.RunKindRoot, Mode: "yolo"}}
	if err := db.SaveRun(parent); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, nil)
	spec, _, err := manager.childSpec(parent.ID, Request{Description: "search", Prompt: "search", SubagentType: "Explore"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Mode != "build" {
		t.Fatalf("Explore inherited stale mode %q", spec.Mode)
	}
	if spec.Kind != model.RunKindSubagent || spec.Depth != 1 {
		t.Fatalf("unexpected lineage: %#v", spec)
	}
}
