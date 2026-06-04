package vector

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
)

// SQLiteVec implements the Store interface using SQLite with a BLOB-based vector store.
// This is a pure-Go implementation that stores embeddings as BLOBs and performs
// brute-force cosine similarity search. For larger datasets, consider using
// sqlite-vec extension or an external vector database.
type SQLiteVec struct {
	db         *sql.DB
	dimensions int
}

// NewSQLiteVec creates a new SQLite-based vector store.
func NewSQLiteVec(db *sql.DB, dimensions int) (*SQLiteVec, error) {
	store := &SQLiteVec{
		db:         db,
		dimensions: dimensions,
	}

	// Ensure the vector_store table exists (it should be created by the main migration)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS vector_store (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_type TEXT NOT NULL,
		source_id TEXT NOT NULL,
		embedding BLOB,
		content TEXT,
		content_summary TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return nil, fmt.Errorf("ensure vector_store table: %w", err)
	}

	// Ensure index
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_vector_store_source ON vector_store(source_type, source_id)`)
	if err != nil {
		return nil, fmt.Errorf("ensure vector_store index: %w", err)
	}

	return store, nil
}

// Upsert inserts or updates vector items.
func (s *SQLiteVec) Upsert(ctx context.Context, items []Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	delStmt, err := tx.PrepareContext(ctx, `DELETE FROM vector_store WHERE source_type = ? AND source_id = ?`)
	if err != nil {
		return err
	}
	defer delStmt.Close()

	insStmt, err := tx.PrepareContext(ctx, `INSERT INTO vector_store (source_type, source_id, embedding, content, content_summary) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insStmt.Close()

	for _, item := range items {
		// Delete existing
		if _, err := delStmt.Exec(item.SourceType, item.SourceID); err != nil {
			return err
		}

		// Encode embedding as float32 BLOB
		embeddingBlob := float32sToBytes(item.Embedding)

		if _, err := insStmt.Exec(item.SourceType, item.SourceID, embeddingBlob, item.Content, item.ContentSummary); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Search performs a brute-force cosine similarity search.
func (s *SQLiteVec) Search(ctx context.Context, query []float32, limit int, sourceType string) ([]SearchResult, error) {
	queryStr := `SELECT source_type, source_id, embedding, content, content_summary FROM vector_store`
	args := []any{}

	if sourceType != "" {
		queryStr += ` WHERE source_type = ?`
		args = append(args, sourceType)
	}

	rows, err := s.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("query vector_store: %w", err)
	}
	defer rows.Close()

	type scored struct {
		result SearchResult
		score  float32
	}

	var results []scored

	for rows.Next() {
		var st, sid, content, summary string
		var embeddingBlob []byte

		if err := rows.Scan(&st, &sid, &embeddingBlob, &content, &summary); err != nil {
			return nil, err
		}

		embedding := bytesToFloat32s(embeddingBlob)
		if len(embedding) != len(query) {
			continue // Skip vectors with different dimensions
		}

		score := cosineSimilarity(query, embedding)

		results = append(results, scored{
			result: SearchResult{
				SourceType:     st,
				SourceID:       sid,
				Content:        content,
				ContentSummary: summary,
				Score:          score,
			},
			score: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by score descending (simple selection sort for now)
	for i := 0; i < len(results)-1; i++ {
		maxIdx := i
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[maxIdx].score {
				maxIdx = j
			}
		}
		if maxIdx != i {
			results[i], results[maxIdx] = results[maxIdx], results[i]
		}
	}

	// Limit results
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	// Extract results
	final := make([]SearchResult, len(results))
	for i, r := range results {
		final[i] = r.result
	}

	return final, nil
}

// Delete removes vectors by source type and ID.
func (s *SQLiteVec) Delete(ctx context.Context, sourceType string, sourceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM vector_store WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID)
	return err
}

// Close is a no-op for SQLiteVec since it shares the main database connection.
func (s *SQLiteVec) Close() error {
	return nil
}

// --- Helper functions ---

func float32sToBytes(floats []float32) []byte {
	buf := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func bytesToFloat32s(data []byte) []float32 {
	if len(data)%4 != 0 {
		return nil
	}
	floats := make([]float32, len(data)/4)
	for i := range floats {
		floats[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return floats
}

func cosineSimilarity(a, b []float32) float32 {
	var dotProduct, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dotProduct += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))
}
