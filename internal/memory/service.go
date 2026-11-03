package memory

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"agent-core/internal/store/vector"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// Service manages durable memories and their optional vector index.
type Service struct {
	store         *store.SQLite
	vecStore      vector.Store
	embedder      vector.Embedder
	cfg           config.MemoryConfig
	mu            sync.Mutex
	consolidating bool
}

func NewService(s *store.SQLite, vs vector.Store, emb vector.Embedder, cfg config.MemoryConfig) *Service {
	return &Service{store: s, vecStore: vs, embedder: emb, cfg: cfg}
}

func (s *Service) Add(ctx context.Context, mem *model.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(ctx, mem, true)
}

func (s *Service) add(ctx context.Context, mem *model.Memory, enforceCapacity bool) error {
	if strings.TrimSpace(mem.Content) == "" {
		return errors.New("memory content is required")
	}
	if enforceCapacity && s.cfg.CapacityLimit > 0 {
		count, err := s.store.CountMemories()
		if err != nil {
			return err
		}
		if count >= s.cfg.CapacityLimit {
			return fmt.Errorf("memory capacity limit %d reached; consolidate or delete memories first", s.cfg.CapacityLimit)
		}
	}
	var embedding []float32
	var err error
	if s.embedder != nil && s.vecStore != nil {
		embedding, err = s.embedder.EmbedOne(ctx, mem.Content)
		if err != nil {
			return fmt.Errorf("embed memory before save: %w", err)
		}
	}
	if err := s.store.AddMemory(mem); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	if embedding != nil {
		if err := s.vecStore.Upsert(ctx, []vector.Item{vectorItem(mem, embedding)}); err != nil {
			_ = s.store.DeleteMemory(mem.ID)
			return fmt.Errorf("index memory; database insert rolled back: %w", err)
		}
	}
	if s.cfg.AutoConsolidate && !s.consolidating && s.cfg.ConsolidationThreshold > 0 {
		count, err := s.store.CountMemories()
		if err == nil && count >= s.cfg.ConsolidationThreshold {
			_, err = s.consolidate(ctx)
			if err != nil {
				return err
			}
		}
	} else if s.cfg.AutoNudge {
		log.Printf("[memory] memory saved; consolidation threshold=%d capacity=%d", s.cfg.ConsolidationThreshold, s.cfg.CapacityLimit)
	}
	return nil
}

func (s *Service) Search(ctx context.Context, query string, limit int) ([]vector.SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	if s.embedder == nil || s.vecStore == nil {
		return s.searchByText(ctx, query, limit)
	}
	embedding, err := s.embedder.EmbedOne(ctx, query)
	if err != nil {
		log.Printf("[memory] query embedding failed; using text fallback: %v", err)
		return s.searchByText(ctx, query, limit)
	}
	candidates, err := s.vecStore.Search(ctx, embedding, limit*4, "memory")
	if err != nil {
		log.Printf("[memory] vector query failed; using text fallback: %v", err)
		return s.searchByText(ctx, query, limit)
	}
	results := make([]vector.SearchResult, 0, limit)
	for _, candidate := range candidates {
		id, err := strconv.ParseInt(candidate.SourceID, 10, 64)
		if err != nil {
			continue
		}
		mem, err := s.store.GetMemory(id)
		if err != nil || mem == nil || !mem.Enabled {
			continue
		}
		candidate.Content = mem.Content
		results = append(results, candidate)
		if len(results) == limit {
			break
		}
	}
	return results, nil
}

func (s *Service) searchByText(_ context.Context, query string, limit int) ([]vector.SearchResult, error) {
	enabled := true
	fetchLimit := s.cfg.CapacityLimit * 2
	if fetchLimit < 200 {
		fetchLimit = 200
	}
	memories, err := s.store.GetMemories("", &enabled, fetchLimit)
	if err != nil {
		return nil, err
	}
	terms := queryTerms(query)
	type scored struct {
		result vector.SearchResult
		score  float32
	}
	var matches []scored
	for _, mem := range memories {
		haystack := strings.ToLower(mem.Content + " " + mem.Category + " " + mem.Source)
		score := float32(mem.Importance) * 0.01
		matched := len(terms) == 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score += 1
				matched = true
			}
		}
		if matched {
			matches = append(matches, scored{result: vector.SearchResult{SourceType: "memory",
				SourceID: strconv.FormatInt(mem.ID, 10), Content: mem.Content}, score: score})
		}
	}
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}
	results := make([]vector.SearchResult, 0, limit)
	for i, match := range matches {
		if i >= limit {
			break
		}
		match.result.Score = match.score
		results = append(results, match.result)
	}
	return results, nil
}

func queryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(query)), func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '.' || r == '，' || r == '。' || r == '?' || r == '？'
	})
}

func (s *Service) GetAll(category string, enabled *bool, limit int) ([]*model.Memory, error) {
	return s.store.GetMemories(category, enabled, limit)
}

func (s *Service) Update(ctx context.Context, mem *model.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.store.GetMemory(mem.ID)
	if err != nil || previous == nil {
		if err == nil {
			err = errors.New("memory not found")
		}
		return err
	}
	var embedding []float32
	if s.embedder != nil && s.vecStore != nil {
		embedding, err = s.embedder.EmbedOne(ctx, mem.Content)
		if err != nil {
			return err
		}
	}
	if err := s.store.UpdateMemory(mem); err != nil {
		return err
	}
	if s.vecStore != nil && !mem.Enabled {
		if err := s.vecStore.Delete(ctx, "memory", strconv.FormatInt(mem.ID, 10)); err != nil {
			return fmt.Errorf("disable memory vector: %w", err)
		}
		return nil
	}
	if embedding != nil {
		if err := s.vecStore.Upsert(ctx, []vector.Item{vectorItem(mem, embedding)}); err != nil {
			_ = s.store.UpdateMemory(previous)
			return fmt.Errorf("update vector index; database update rolled back: %w", err)
		}
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.DeleteMemory(id); err != nil {
		return err
	}
	if s.vecStore != nil {
		if err := s.vecStore.Delete(ctx, "memory", strconv.FormatInt(id, 10)); err != nil {
			return fmt.Errorf("memory deleted but vector cleanup failed; rebuild required: %w", err)
		}
	}
	return nil
}

// Consolidate merges the lowest-ranked enabled memories into one durable entry.
func (s *Service) Consolidate(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consolidate(ctx)
}

func (s *Service) consolidate(ctx context.Context) (int, error) {
	s.consolidating = true
	defer func() { s.consolidating = false }()
	enabled := true
	memories, err := s.store.GetMemories("", &enabled, s.cfg.CapacityLimit*2+100)
	if err != nil {
		return 0, err
	}
	threshold := s.cfg.ConsolidationThreshold
	if threshold <= 0 || len(memories) <= threshold {
		return 0, nil
	}
	batch := len(memories) - threshold + 1
	victims := memories[len(memories)-batch:]
	var content strings.Builder
	importance := 0.0
	for _, mem := range victims {
		fmt.Fprintf(&content, "- %s\n", truncate(mem.Content, 500))
		importance += mem.Importance
	}
	merged := &model.Memory{Content: "Consolidated memories:\n" + content.String(), Category: "consolidated",
		Source: "consolidation", Importance: importance / float64(len(victims)), Enabled: true}
	if err := s.add(ctx, merged, false); err != nil {
		return 0, err
	}
	var errs []error
	for _, mem := range victims {
		if err := s.store.DeleteMemory(mem.ID); err != nil {
			errs = append(errs, err)
		}
		if s.vecStore != nil {
			if err := s.vecStore.Delete(ctx, "memory", strconv.FormatInt(mem.ID, 10)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return len(victims), errors.Join(errs...)
}

// Rebuild reindexes current memories and removes vectors for disabled entries.
func (s *Service) Rebuild(ctx context.Context) error {
	if s.embedder == nil || s.vecStore == nil {
		return nil
	}
	memories, err := s.store.GetMemories("", nil, 100_000)
	if err != nil {
		return err
	}
	var errs []error
	for _, mem := range memories {
		if !mem.Enabled {
			if err := s.vecStore.Delete(ctx, "memory", strconv.FormatInt(mem.ID, 10)); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		embedding, err := s.embedder.EmbedOne(ctx, mem.Content)
		if err == nil {
			err = s.vecStore.Upsert(ctx, []vector.Item{vectorItem(mem, embedding)})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("reindex memory %d: %w", mem.ID, err))
		}
	}
	return errors.Join(errs...)
}

func vectorItem(mem *model.Memory, embedding []float32) vector.Item {
	return vector.Item{SourceType: "memory", SourceID: strconv.FormatInt(mem.ID, 10),
		Embedding: embedding, Content: mem.Content, ContentSummary: truncate(mem.Content, 200)}
}

func (s *Service) Count() (int, error) { return s.store.CountMemories() }

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "..."
}
