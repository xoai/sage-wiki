package postgres

import (
	"database/sql"

	"github.com/pgvector/pgvector-go"

	"github.com/xoai/sage-wiki/internal/store"
)

type vectorStore struct{ b *backend }

var _ store.VectorStore = (*vectorStore)(nil)

// InvalidateChunkCache is a no-op on postgres: search is DB-side, there is
// no in-memory cache (design D4 — the contract survives the seam, the work
// does not).
func (s *vectorStore) InvalidateChunkCache() {}

func (s *vectorStore) Upsert(id string, embedding []float32) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO vec_entries (id, embedding, dimensions) VALUES ($1, $2, $3)
			ON CONFLICT (id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
			id, pgvector.NewVector(embedding), len(embedding))
		return err
	})
}

// Get: (nil, nil) when absent — legacy contract (spec §3).
func (s *vectorStore) Get(id string) ([]float32, error) {
	var v pgvector.Vector
	err := s.b.pool.QueryRow("SELECT embedding FROM vec_entries WHERE id=$1", id).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v.Slice(), nil
}

func (s *vectorStore) Delete(id string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_entries WHERE id=$1", id)
		return err
	})
}

func (s *vectorStore) Search(query []float32, limit int) ([]store.VectorResult, error) {
	limit = normLimit(limit, 10)
	// Dimension guard, GO-SIDE (vectors/store.go:229-234 parity): a nil or
	// mismatched query matches nothing. Must not rely on the SQL predicate
	// alone — an HNSW KNN scan computes distances inside the index BEFORE
	// the filter is applied and would error on mismatched dims.
	if len(query) == 0 || len(query) != s.b.opts.VectorDimension {
		return nil, nil
	}
	rows, err := s.b.pool.Query(`
		SELECT id, 1 - (embedding <=> $1) AS score FROM vec_entries
		WHERE dimensions = $2
		ORDER BY embedding <=> $1 LIMIT $3`, pgvector.NewVector(query), len(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.VectorResult
	for rows.Next() {
		var r store.VectorResult
		var score float64
		if err := rows.Scan(&r.ID, &score); err != nil {
			return nil, err
		}
		r.Score = score
		r.Rank = len(out) + 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *vectorStore) UpsertChunk(tx *sql.Tx, chunkID string, docID string, embedding []float32) error {
	_, err := tx.Exec(`
		INSERT INTO vec_chunks (chunk_id, doc_id, embedding, dimensions) VALUES ($1, $2, $3, $4)
		ON CONFLICT (chunk_id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
		chunkID, docID, pgvector.NewVector(embedding), len(embedding))
	return err
}

func (s *vectorStore) SearchChunks(query []float32, limit int) ([]store.ChunkVectorResult, error) {
	limit = normLimit(limit, 20)
	if len(query) == 0 || len(query) != s.b.opts.VectorDimension {
		return nil, nil
	}
	rows, err := s.b.pool.Query(`
		SELECT chunk_id, doc_id, 1 - (embedding <=> $1) AS score FROM vec_chunks
		WHERE dimensions = $2
		ORDER BY embedding <=> $1 LIMIT $3`, pgvector.NewVector(query), len(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkVectors(rows)
}

// SearchChunksFiltered: DB-side docID filter; the 100-docID cap from the
// sqlite cache path is interface contract (spec §4) and applied here too.
func (s *vectorStore) SearchChunksFiltered(query []float32, docIDs []string, limit int) ([]store.ChunkVectorResult, error) {
	limit = normLimit(limit, 20)
	if len(docIDs) == 0 {
		return nil, nil
	}
	if len(docIDs) > 100 {
		docIDs = docIDs[:100]
	}
	if len(query) == 0 || len(query) != s.b.opts.VectorDimension {
		return nil, nil
	}
	rows, err := s.b.pool.Query(`
		SELECT chunk_id, doc_id, 1 - (embedding <=> $1) AS score FROM vec_chunks
		WHERE doc_id = ANY($2) AND dimensions = $3
		ORDER BY embedding <=> $1 LIMIT $4`, pgvector.NewVector(query), docIDs, len(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkVectors(rows)
}

func scanChunkVectors(rows *sql.Rows) ([]store.ChunkVectorResult, error) {
	var out []store.ChunkVectorResult
	for rows.Next() {
		var r store.ChunkVectorResult
		var score float64
		if err := rows.Scan(&r.ChunkID, &r.DocID, &score); err != nil {
			return nil, err
		}
		r.Score = score
		r.Rank = len(out) + 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *vectorStore) DeleteDocChunkVectors(docID string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_chunks WHERE doc_id=$1", docID)
		return err
	})
}

func (s *vectorStore) HasChunkVectors(docID string) (bool, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM vec_chunks WHERE doc_id=$1", docID).Scan(&n)
	return n > 0, err
}

func (s *vectorStore) Count() (int, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM vec_entries").Scan(&n)
	return n, err
}

func (s *vectorStore) Dimensions() (int, error) {
	var n sql.NullInt64
	err := s.b.pool.QueryRow("SELECT MAX(dimensions) FROM vec_entries").Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n.Int64), nil
}
