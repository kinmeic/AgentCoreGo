package memory

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"context"
	"path/filepath"
	"testing"
)

func newMemoryTestService(t *testing.T, cfg config.MemoryConfig) *Service {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db, cfg)
}

func TestTextSearchUsesQueryAndCapacityIsEnforced(t *testing.T) {
	service := newMemoryTestService(t, config.MemoryConfig{CapacityLimit: 3, ConsolidationThreshold: 2})
	for _, content := range []string{"unrelated fact", "needle in a memory", "another fact"} {
		if err := service.Add(context.Background(), &model.Memory{Content: content, Enabled: true, Importance: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	results, err := service.Search(context.Background(), "needle", 5)
	if err != nil || len(results) != 1 || results[0].Content != "needle in a memory" {
		t.Fatalf("unexpected text search: %#v, %v", results, err)
	}
	if err := service.Add(context.Background(), &model.Memory{Content: "over capacity", Enabled: true}); err == nil {
		t.Fatal("capacity limit was not enforced")
	}
}

func TestConsolidateActuallyReducesMemories(t *testing.T) {
	service := newMemoryTestService(t, config.MemoryConfig{CapacityLimit: 10, ConsolidationThreshold: 3})
	for _, content := range []string{"one", "two", "three", "four"} {
		if err := service.Add(context.Background(), &model.Memory{Content: content, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := service.Consolidate(context.Background())
	if err != nil || merged != 2 {
		t.Fatalf("merged=%d err=%v", merged, err)
	}
	count, err := service.Count()
	if err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	memories, err := service.GetAll("consolidated", nil, 10)
	if err != nil || len(memories) != 1 {
		t.Fatalf("missing consolidated memory: %#v, %v", memories, err)
	}
}

func TestTextSearchFiltersDisabledMemories(t *testing.T) {
	service := newMemoryTestService(t, config.MemoryConfig{})
	first := &model.Memory{Content: "needle enabled", Enabled: true}
	second := &model.Memory{Content: "needle disabled", Enabled: false}
	for _, item := range []*model.Memory{first, second} {
		if err := service.Add(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	results, err := service.Search(context.Background(), "needle", 5)
	if err != nil || len(results) != 1 || results[0].Content != first.Content {
		t.Fatalf("disabled memory leaked: %#v %v", results, err)
	}
}
