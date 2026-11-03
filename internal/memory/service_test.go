package memory

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/store/vector"
	"context"
	"path/filepath"
	"testing"
)

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedOne(context.Context, string) ([]float32, error) { return []float32{1}, nil }

type fakeVector struct{ items map[string]vector.Item }

func newFakeVector() *fakeVector { return &fakeVector{items: make(map[string]vector.Item)} }
func (f *fakeVector) Upsert(_ context.Context, items []vector.Item) error {
	for _, item := range items {
		f.items[item.SourceID] = item
	}
	return nil
}
func (f *fakeVector) Search(_ context.Context, _ []float32, limit int, _ string) ([]vector.SearchResult, error) {
	var results []vector.SearchResult
	for _, item := range f.items {
		results = append(results, vector.SearchResult{SourceType: item.SourceType, SourceID: item.SourceID, Content: item.Content})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}
func (f *fakeVector) Delete(_ context.Context, _ string, id string) error {
	delete(f.items, id)
	return nil
}
func (f *fakeVector) Close() error { return nil }

func newMemoryTestService(t *testing.T, cfg config.MemoryConfig, vectors vector.Store, embedder vector.Embedder) *Service {
	t.Helper()
	db, err := store.NewSQLite(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db, vectors, embedder, cfg)
}

func TestTextSearchUsesQueryAndCapacityIsEnforced(t *testing.T) {
	service := newMemoryTestService(t, config.MemoryConfig{CapacityLimit: 3, ConsolidationThreshold: 2}, nil, nil)
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
	service := newMemoryTestService(t, config.MemoryConfig{CapacityLimit: 10, ConsolidationThreshold: 3}, nil, nil)
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

func TestVectorSearchFiltersDisabledMemories(t *testing.T) {
	vectors := newFakeVector()
	service := newMemoryTestService(t, config.MemoryConfig{CapacityLimit: 10}, vectors, fakeEmbedder{})
	first := &model.Memory{Content: "enabled", Enabled: true}
	second := &model.Memory{Content: "disabled", Enabled: true}
	if err := service.Add(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := service.Add(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	second.Enabled = false
	if err := service.Update(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Content != "enabled" {
		t.Fatalf("disabled memory leaked into results: %#v", results)
	}
}
