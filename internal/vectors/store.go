package vectors

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/xoai/sage-wiki/internal/storage"
)

// Store manages vector embeddings as BLOBs in SQLite.
type Store struct {
	db *storage.DB
}

// NewStore creates a new vector store.
func NewStore(db *storage.DB) *Store {
	return &Store{db: db}
}

// Upsert stores or replaces a vector for the given ID.
func (s *Store) Upsert(id string, embedding []float32) error {
	blob := encodeFloat32s(embedding)
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
			id, blob, len(embedding),
		)
		return err
	})
}

// Delete removes a vector by ID.
func (s *Store) Delete(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_entries WHERE id=?", id)
		return err
	})
}

// VectorResult represents a cosine similarity search result.
type VectorResult struct {
	ID    string
	Score float64
	Rank  int
}

// Search performs brute-force cosine similarity search.
func (s *Store) Search(query []float32, limit int) ([]VectorResult, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.ReadDB().Query("SELECT id, embedding, dimensions FROM vec_entries")
	if err != nil {
		return nil, fmt.Errorf("vectors.Search: %w", err)
	}
	defer rows.Close()

	var results []VectorResult
	for rows.Next() {
		var id string
		var blob []byte
		var dims int
		if err := rows.Scan(&id, &blob, &dims); err != nil {
			return nil, err
		}

		vec := decodeFloat32s(blob)
		if len(vec) != len(query) {
			continue // dimension mismatch, skip
		}

		score := CosineSimilarity(query, vec)
		results = insertSorted(results, VectorResult{ID: id, Score: score}, limit)
	}

	// Assign ranks
	for i := range results {
		results[i].Rank = i + 1
	}

	return results, rows.Err()
}

// UpsertChunk stores or replaces a chunk vector within an existing transaction.
func (s *Store) UpsertChunk(tx *sql.Tx, chunkID string, docID string, embedding []float32) error {
	blob := encodeFloat32s(embedding)
	_, err := tx.Exec(
		`INSERT INTO vec_chunks (chunk_id, doc_id, embedding, dimensions) VALUES (?, ?, ?, ?)
		 ON CONFLICT(chunk_id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
		chunkID, docID, blob, len(embedding),
	)
	return err
}

// SearchChunks performs brute-force cosine similarity search on chunk vectors.
func (s *Store) SearchChunks(query []float32, limit int) ([]ChunkVectorResult, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.ReadDB().Query("SELECT chunk_id, doc_id, embedding, dimensions FROM vec_chunks")
	if err != nil {
		return nil, fmt.Errorf("vectors.SearchChunks: %w", err)
	}
	defer rows.Close()

	var results []ChunkVectorResult
	for rows.Next() {
		var chunkID, docID string
		var blob []byte
		var dims int
		if err := rows.Scan(&chunkID, &docID, &blob, &dims); err != nil {
			return nil, err
		}
		vec := decodeFloat32s(blob)
		if len(vec) != len(query) {
			continue
		}
		score := CosineSimilarity(query, vec)
		results = insertChunkSorted(results, ChunkVectorResult{ChunkID: chunkID, DocID: docID, Score: score}, limit)
	}

	for i := range results {
		results[i].Rank = i + 1
	}
	return results, rows.Err()
}

// SearchChunksFiltered performs cosine search only on chunks belonging to the given doc IDs.
// This is the BM25-prefiltered path that caps vector comparisons.
func (s *Store) SearchChunksFiltered(query []float32, docIDs []string, limit int) ([]ChunkVectorResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(docIDs) == 0 {
		return nil, nil
	}

	// Cap doc IDs to 100
	if len(docIDs) > 100 {
		docIDs = docIDs[:100]
	}

	// Build query with IN clause
	ph := make([]string, len(docIDs))
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		ph[i] = "?"
		args[i] = id
	}
	sqlStr := fmt.Sprintf(
		"SELECT chunk_id, doc_id, embedding, dimensions FROM vec_chunks WHERE doc_id IN (%s)",
		strings.Join(ph, ","),
	)

	rows, err := s.db.ReadDB().Query(sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("vectors.SearchChunksFiltered: %w", err)
	}
	defer rows.Close()

	var results []ChunkVectorResult
	for rows.Next() {
		var chunkID, docID string
		var blob []byte
		var dims int
		if err := rows.Scan(&chunkID, &docID, &blob, &dims); err != nil {
			return nil, err
		}
		vec := decodeFloat32s(blob)
		if len(vec) != len(query) {
			continue
		}
		score := CosineSimilarity(query, vec)
		results = insertChunkSorted(results, ChunkVectorResult{ChunkID: chunkID, DocID: docID, Score: score}, limit)
	}

	for i := range results {
		results[i].Rank = i + 1
	}
	return results, rows.Err()
}

// DeleteDocChunkVectors removes all chunk vectors for a document.
func (s *Store) DeleteDocChunkVectors(docID string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_chunks WHERE doc_id = ?", docID)
		return err
	})
}

// ChunkVectorResult represents a chunk cosine similarity search result.
type ChunkVectorResult struct {
	ChunkID string
	DocID   string
	Score   float64
	Rank    int
}

// insertChunkSorted maintains a sorted slice of top-k chunk results (descending by score).
func insertChunkSorted(results []ChunkVectorResult, item ChunkVectorResult, limit int) []ChunkVectorResult {
	pos := len(results)
	for pos > 0 && results[pos-1].Score < item.Score {
		pos--
	}
	if pos >= limit {
		return results
	}
	if len(results) < limit {
		results = append(results, ChunkVectorResult{})
	}
	copy(results[pos+1:], results[pos:])
	results[pos] = item
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// Count returns the total number of stored vectors.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM vec_entries").Scan(&count)
	return count, err
}

// Dimensions returns the dimension count of the first stored vector, or 0 if empty.
func (s *Store) Dimensions() (int, error) {
	var dims int
	err := s.db.ReadDB().QueryRow("SELECT COALESCE(MAX(dimensions), 0) FROM vec_entries").Scan(&dims)
	return dims, err
}

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// encodeFloat32s converts []float32 to []byte (little-endian).
func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeFloat32s converts []byte (little-endian) to []float32.
func decodeFloat32s(buf []byte) []float32 {
	v := make([]float32, len(buf)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}

// insertSorted maintains a sorted slice of top-k results (descending by score).
func insertSorted(results []VectorResult, item VectorResult, limit int) []VectorResult {
	pos := len(results)
	for pos > 0 && results[pos-1].Score < item.Score {
		pos--
	}

	if pos >= limit {
		return results
	}

	if len(results) < limit {
		results = append(results, VectorResult{})
	}

	copy(results[pos+1:], results[pos:])
	results[pos] = item

	if len(results) > limit {
		results = results[:limit]
	}

	return results
}
