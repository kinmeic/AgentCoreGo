package tool

import (
	"sort"
	"sync"
)

// DiscoveryState tracks tools explicitly discovered during one run.
type DiscoveryState struct {
	mu          sync.RWMutex
	Progressive bool
	Threshold   int
	discovered  map[string]bool
}

func NewDiscoveryState(names []string, progressive bool, threshold int) *DiscoveryState {
	if threshold <= 0 {
		threshold = SearchThreshold
	}
	state := &DiscoveryState{Progressive: progressive, Threshold: threshold, discovered: make(map[string]bool)}
	for _, name := range names {
		state.discovered[name] = true
	}
	return state
}

func (s *DiscoveryState) Discover(names ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		s.discovered[name] = true
	}
}

func (s *DiscoveryState) Visible(name string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discovered[name]
}

func (s *DiscoveryState) Names() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.discovered))
	for name := range s.discovered {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
