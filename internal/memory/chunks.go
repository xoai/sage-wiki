package memory

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/xoai/sage-wiki/internal/storage"
)

// ChunkEntry represents a chunk to be indexed.
type ChunkEntry struct {
	ChunkID     string
	ChunkIndex  int
	Heading     string
	Content     string
	StartOffset int
	EndOffset   int
}

// ChunkResult represents a chunk search hit.
type ChunkResult struct {
	ChunkID   string
	DocID     string
	Heading   string
	Content   string
	BM25Score float64
	Rank      int
}

// ChunkStore manages chunk-level FTS5 entries.
type ChunkStore struct {
	db *storage.DB
}

// NewChunkStore creates a new chunk store.
func NewChunkStore(db *storage.DB) *ChunkStore {
	return &ChunkStore{db: db}
}

// IndexChunks inserts chunks for a document within a write transaction.
// Callers should delete old chunks first via DeleteDocChunks.
func (s *ChunkStore) IndexChunks(tx *sql.Tx, docID string, chunks []ChunkEntry) error {
	for _, c := range chunks {
		if _, err := tx.Exec(
			"INSERT INTO chunks_meta (chunk_id, doc_id, chunk_index, heading, content, start_offset, end_offset) VALUES (?, ?, ?, ?, ?, ?, ?)",
			c.ChunkID, docID, c.ChunkIndex, c.Heading, c.Content, c.StartOffset, c.EndOffset,
		); err != nil {
			return fmt.Errorf("chunks.IndexChunks meta: %w", err)
		}
		if _, err := tx.Exec(
			"INSERT INTO chunks_fts (chunk_id, heading, content) VALUES (?, ?, ?)",
			c.ChunkID, c.Heading, c.Content,
		); err != nil {
			return fmt.Errorf("chunks.IndexChunks fts: %w", err)
		}
	}
	return nil
}

// DeleteDocChunks removes all chunks for a document within a write transaction.
func (s *ChunkStore) DeleteDocChunks(tx *sql.Tx, docID string) error {
	if _, err := tx.Exec(
		"DELETE FROM chunks_fts WHERE chunk_id IN (SELECT chunk_id FROM chunks_meta WHERE doc_id = ?)", docID,
	); err != nil {
		return fmt.Errorf("chunks.DeleteDocChunks fts: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM chunks_meta WHERE doc_id = ?", docID); err != nil {
		return fmt.Errorf("chunks.DeleteDocChunks meta: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM vec_chunks WHERE doc_id = ?", docID); err != nil {
		return fmt.Errorf("chunks.DeleteDocChunks vec: %w", err)
	}
	return nil
}

// SearchChunks performs BM25 search on chunks, returning results ranked by relevance.
func (s *ChunkStore) SearchChunks(query string, limit int) ([]ChunkResult, error) {
	if limit <= 0 {
		limit = 20
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.ReadDB().Query(`
		SELECT f.chunk_id, m.doc_id, f.heading, f.content, f.rank
		FROM chunks_fts f
		JOIN chunks_meta m ON m.chunk_id = f.chunk_id
		WHERE chunks_fts MATCH ?
		ORDER BY f.rank
		LIMIT ?
	`, ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("chunks.SearchChunks: %w", err)
	}
	defer rows.Close()

	var results []ChunkResult
	rank := 1
	for rows.Next() {
		var r ChunkResult
		var bm25 float64
		if err := rows.Scan(&r.ChunkID, &r.DocID, &r.Heading, &r.Content, &bm25); err != nil {
			return nil, err
		}
		r.BM25Score = -bm25
		r.Rank = rank
		rank++
		results = append(results, r)
	}
	return results, rows.Err()
}

// Count returns the total number of indexed chunks.
func (s *ChunkStore) Count() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM chunks_meta").Scan(&count)
	return count, err
}

// DocIDs returns unique document IDs from a set of chunk results.
func DocIDs(results []ChunkResult) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, r := range results {
		if !seen[r.DocID] {
			seen[r.DocID] = true
			ids = append(ids, r.DocID)
		}
	}
	return ids
}

// SearchChunksMultiQuery runs BM25 search for multiple query variants and merges via RRF.
func (s *ChunkStore) SearchChunksMultiQuery(queries []string, limit int) ([]ChunkResult, error) {
	if len(queries) == 0 {
		return nil, nil
	}
	if len(queries) == 1 {
		return s.SearchChunks(queries[0], limit)
	}

	// Search each variant
	type scoredChunk struct {
		result ChunkResult
		rrf    float64
	}
	chunkMap := make(map[string]*scoredChunk)
	const k = 60.0

	for _, q := range queries {
		results, err := s.SearchChunks(q, limit)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			sc, ok := chunkMap[r.ChunkID]
			if !ok {
				sc = &scoredChunk{result: r}
				chunkMap[r.ChunkID] = sc
			}
			sc.rrf += 1.0 / (k + float64(r.Rank))
		}
	}

	// Sort by RRF score
	sorted := make([]ChunkResult, 0, len(chunkMap))
	for _, sc := range chunkMap {
		sc.result.BM25Score = sc.rrf
		sorted = append(sorted, sc.result)
	}

	// Sort descending by score
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].BM25Score > sorted[j].BM25Score
	})

	// Re-rank
	for i := range sorted {
		sorted[i].Rank = i + 1
	}

	if len(sorted) > limit {
		sorted = sorted[:limit]
	}
	return sorted, nil
}

// NeedsBackfill returns true if chunk index is empty but entries exist.
func (s *ChunkStore) NeedsBackfill(memStore *Store) bool {
	chunkCount, err := s.Count()
	if err != nil || chunkCount > 0 {
		return false
	}
	entryCount, err := memStore.Count()
	return err == nil && entryCount > 0
}

