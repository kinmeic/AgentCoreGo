package memory

import (
	"agent-core/internal/config"
	"agent-core/internal/model"
	"agent-core/internal/store"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// Service manages local text memories without a remote embedding dependency.
type Service struct {
	store         *store.SQLite
	cfg           config.MemoryConfig
	mu            sync.Mutex
	consolidating bool
}

func NewService(s *store.SQLite, cfg config.MemoryConfig) *Service {
	return &Service{store: s, cfg: cfg}
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
	if err := s.store.AddMemory(mem); err != nil {
		return fmt.Errorf("save memory: %w", err)
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

func (s *Service) Search(ctx context.Context, query string, limit int) ([]model.MemorySearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	return s.searchByText(ctx, query, limit)
}

func (s *Service) searchByText(_ context.Context, query string, limit int) ([]model.MemorySearchResult, error) {
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
		result model.MemorySearchResult
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
			matches = append(matches, scored{result: model.MemorySearchResult{SourceType: "memory",
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
	results := make([]model.MemorySearchResult, 0, limit)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(mem.Content) == "" {
		return errors.New("memory content is required")
	}
	previous, err := s.store.GetMemory(mem.ID)
	if err != nil {
		return err
	}
	if previous == nil {
		return errors.New("memory not found")
	}
	return s.store.UpdateMemory(mem)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.DeleteMemory(id)
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
	}
	return len(victims), errors.Join(errs...)
}

func (s *Service) Count() (int, error) { return s.store.CountMemories() }

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "..."
}
