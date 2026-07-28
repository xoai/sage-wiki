package memory

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/xoai/sage-wiki/internal/store"
)

// Countable is aliased to store.Countable (P2-1 D2-prime relocation).
type Countable = store.Countable

// ChunkEntry represents a chunk to be indexed.
type ChunkEntry = store.ChunkEntry

// ChunkEntryWithDoc is a ChunkEntry plus its owning doc ID (ListAll rows).
type ChunkEntryWithDoc = store.ChunkEntryWithDoc

// ChunkResult represents a chunk search hit.
type ChunkResult = store.ChunkResult

// ChunkStore manages chunk-level FTS5 entries.
type ChunkStore struct {
	db store.DBHandle
}

// NewChunkStore creates a new chunk store.
func NewChunkStore(db store.DBHandle) *ChunkStore {
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

	// DF pruning probes the DOCUMENT corpus (`entries`), not the chunk
	// tables. Two reasons, both load-bearing: the legs must prune on
	// identical doc-ratio semantics or they diverge (Gate-3 F-047), which
	// probing the same table guarantees by construction rather than by
	// matching two queries; and the chunk-side probe was a COUNT(DISTINCT)
	// over a chunks_fts/chunks_meta join per term — ~66% of unified search
	// time in the V-M5c profile, for a ratio the entries table answers with
	// a plain FTS count.
	ftsQuery := formatFTSTerms(dfPruneTerms(s.db,
		"SELECT COUNT(*) FROM entries",
		"SELECT COUNT(*) FROM entries WHERE entries MATCH ?",
		BuildFTSTerms(query)))
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

// GetChunksMeta returns heading and content for the given chunk IDs.
// Missing IDs are simply absent from the map.
func (s *ChunkStore) GetChunksMeta(ids []string) (map[string]ChunkEntry, error) {
	if len(ids) == 0 {
		return map[string]ChunkEntry{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.ReadDB().Query(
		"SELECT chunk_id, chunk_index, heading, content FROM chunks_meta WHERE chunk_id IN ("+placeholders+")",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("chunks.GetChunksMeta: %w", err)
	}
	defer rows.Close()

	out := make(map[string]ChunkEntry, len(ids))
	for rows.Next() {
		var c ChunkEntry
		if err := rows.Scan(&c.ChunkID, &c.ChunkIndex, &c.Heading, &c.Content); err != nil {
			return nil, fmt.Errorf("chunks.GetChunksMeta scan: %w", err)
		}
		out[c.ChunkID] = c
	}
	return out, rows.Err()
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

// NeedsBackfill returns true if chunk index is empty but entries exist.
func (s *ChunkStore) NeedsBackfill(memStore Countable) bool {
	chunkCount, err := s.Count()
	if err != nil || chunkCount > 0 {
		return false
	}
	entryCount, err := memStore.Count()
	return err == nil && entryCount > 0
}

// ListAll returns every chunk, fully populated, ordered for determinism
// (P2-1: absorbs reembed's raw chunks_meta scan). Unbounded by design.
// ListDocIDs returns the distinct doc IDs that currently have chunks.
func (s *ChunkStore) ListDocIDs() ([]string, error) {
	rows, err := s.db.ReadDB().Query("SELECT DISTINCT doc_id FROM chunks_meta ORDER BY doc_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *ChunkStore) ListAll() ([]ChunkEntryWithDoc, error) {
	rows, err := s.db.ReadDB().Query(
		"SELECT chunk_id, doc_id, chunk_index, heading, content, start_offset, end_offset FROM chunks_meta ORDER BY doc_id, chunk_index")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChunkEntryWithDoc
	for rows.Next() {
		var c ChunkEntryWithDoc
		if err := rows.Scan(&c.ChunkID, &c.DocID, &c.ChunkIndex, &c.Heading, &c.Content, &c.StartOffset, &c.EndOffset); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
