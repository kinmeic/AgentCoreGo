package memory

import (
	"agent-core/internal/model"
	"agent-core/internal/config"
	"agent-core/internal/store"
	"agent-core/internal/store/vector"
	"context"
	"fmt"
	"log"
	"strconv"
)

// Service provides memory management for the model.
type Service struct {
	store     *store.SQLite
	vecStore  vector.Store
	embedder  *vector.EmbeddingClient
	cfg       config.MemoryConfig
}

// NewService creates a new memory service.
func NewService(s *store.SQLite, vs vector.Store, emb *vector.EmbeddingClient, cfg config.MemoryConfig) *Service {
	return &Service{
		store:    s,
		vecStore: vs,
		embedder: emb,
		cfg:      cfg,
	}
}

// Add creates a new memory and indexes it for vector search.
func (s *Service) Add(ctx context.Context, mem *model.Memory) error {
	if err := s.store.AddMemory(mem); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}

	// Index for vector search if embedder is available
	if s.embedder != nil && s.vecStore != nil {
		embedding, err := s.embedder.EmbedOne(ctx, mem.Content)
		if err != nil {
			log.Printf("[memory] Failed to embed memory %d: %v", mem.ID, err)
			return nil // Don't fail the whole operation
		}

		err = s.vecStore.Upsert(ctx, []vector.Item{{
			SourceType:     "memory",
			SourceID:       strconv.FormatInt(mem.ID, 10),
			Embedding:      embedding,
			Content:        mem.Content,
			ContentSummary: truncate(mem.Content, 200),
		}})
		if err != nil {
			log.Printf("[memory] Failed to index memory %d: %v", mem.ID, err)
		}
	}

	// Check capacity and consolidate if needed
	count, _ := s.store.CountMemories()
	if count > s.cfg.ConsolidationThreshold && s.cfg.AutoNudge {
		log.Printf("[memory] Memory count (%d) approaching capacity (%d), consider consolidation", count, s.cfg.CapacityLimit)
	}

	return nil
}

// Search finds relevant memories using vector similarity.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]vector.SearchResult, error) {
	if s.embedder == nil || s.vecStore == nil {
		// Fallback to text-based retrieval
		return s.searchByText(ctx, query, limit)
	}

	embedding, err := s.embedder.EmbedOne(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	results, err := s.vecStore.Search(ctx, embedding, limit, "memory")
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	return results, nil
}

// searchByText performs basic text search when vector search is unavailable.
func (s *Service) searchByText(_ context.Context, _ string, limit int) ([]vector.SearchResult, error) {
	enabled := true
	memories, err := s.store.GetMemories("", &enabled, limit)
	if err != nil {
		return nil, err
	}

	results := make([]vector.SearchResult, len(memories))
	for i, m := range memories {
		results[i] = vector.SearchResult{
			SourceType: "memory",
			SourceID:   strconv.FormatInt(m.ID, 10),
			Content:    m.Content,
			Score:      float32(m.Importance),
		}
	}
	return results, nil
}

// GetAll retrieves all memories with optional filtering.
func (s *Service) GetAll(category string, enabled *bool, limit int) ([]*model.Memory, error) {
	return s.store.GetMemories(category, enabled, limit)
}

// Update modifies an existing memory.
func (s *Service) Update(ctx context.Context, mem *model.Memory) error {
	if err := s.store.UpdateMemory(mem); err != nil {
		return err
	}

	// Re-index
	if s.embedder != nil && s.vecStore != nil {
		embedding, err := s.embedder.EmbedOne(ctx, mem.Content)
		if err == nil {
			s.vecStore.Upsert(ctx, []vector.Item{{
				SourceType:     "memory",
				SourceID:       strconv.FormatInt(mem.ID, 10),
				Embedding:      embedding,
				Content:        mem.Content,
				ContentSummary: truncate(mem.Content, 200),
			}})
		}
	}

	return nil
}

// Delete removes a memory and its vector index.
func (s *Service) Delete(ctx context.Context, id int64) error {
	if s.vecStore != nil {
		s.vecStore.Delete(ctx, "memory", strconv.FormatInt(id, 10))
	}
	return s.store.DeleteMemory(id)
}

// Count returns the number of enabled memories.
func (s *Service) Count() (int, error) {
	return s.store.CountMemories()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
