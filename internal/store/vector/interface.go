package vector

import "context"

// Store defines the interface for pluggable vector storage backends.
type Store interface {
	// Upsert inserts or updates vector items.
	Upsert(ctx context.Context, items []Item) error

	// Search performs a nearest-neighbor search.
	Search(ctx context.Context, query []float32, limit int, sourceType string) ([]SearchResult, error)

	// Delete removes vectors by source type and ID.
	Delete(ctx context.Context, sourceType string, sourceID string) error

	// Close releases any resources held by the store.
	Close() error
}

// Item represents a single vector item to be stored.
type Item struct {
	SourceType     string    `json:"source_type"`
	SourceID       string    `json:"source_id"`
	Embedding      []float32 `json:"embedding"`
	Content        string    `json:"content"`
	ContentSummary string    `json:"content_summary"`
}

// SearchResult represents a single search result.
type SearchResult struct {
	SourceType     string  `json:"source_type"`
	SourceID       string  `json:"source_id"`
	Content        string  `json:"content"`
	ContentSummary string  `json:"content_summary"`
	Score          float32 `json:"score"`
}
