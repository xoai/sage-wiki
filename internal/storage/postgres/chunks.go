package postgres

import (
	"database/sql"
	"fmt"

	"github.com/xoai/sage-wiki/internal/store"
)

type chunkStore struct{ b *backend }

var _ store.ChunkStore = (*chunkStore)(nil)

func (s *chunkStore) IndexChunks(tx *sql.Tx, docID string, chunks []store.ChunkEntry) error {
	for _, c := range chunks {
		if _, err := tx.Exec(`
			INSERT INTO chunks_meta (chunk_id, doc_id, chunk_index, heading, content, start_offset, end_offset)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (chunk_id) DO UPDATE SET
				doc_id=excluded.doc_id, chunk_index=excluded.chunk_index,
				heading=excluded.heading, content=excluded.content,
				start_offset=excluded.start_offset, end_offset=excluded.end_offset`,
			c.ChunkID, docID, c.ChunkIndex, c.Heading, c.Content, c.StartOffset, c.EndOffset); err != nil {
			return err
		}
	}
	return nil
}

func (s *chunkStore) DeleteDocChunks(tx *sql.Tx, docID string) error {
	if _, err := tx.Exec("DELETE FROM chunks_meta WHERE doc_id=$1", docID); err != nil {
		return err
	}
	// Cross-table parity with sqlite DeleteDocChunks (chunks.go:71).
	if _, err := tx.Exec("DELETE FROM vec_chunks WHERE doc_id=$1", docID); err != nil {
		return err
	}
	return nil
}

func (s *chunkStore) SearchChunks(query string, limit int) ([]store.ChunkResult, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	where, args, next := s.b.ftsQuery("tsv", terms, 1)
	rank, rankArgs, next := s.b.ftsRank("tsv", terms, next)
	args = append(args, rankArgs...)
	sqlText := fmt.Sprintf(
		"SELECT chunk_id, doc_id, heading, content FROM chunks_meta WHERE %s ORDER BY %s LIMIT $%d",
		where, rank, next)
	args = append(args, limit)
	return s.scanChunkResults(sqlText, args...)
}

func (s *chunkStore) SearchChunksMultiQuery(queries []string, limit int) ([]store.ChunkResult, error) {
	// RRF merge parity with sqlite (chunks.go:138): run each query, fuse by
	// reciprocal rank in Go — the DB-side queries are independent.
	const rrfK = 60.0
	scores := map[string]float64{}
	var byID map[string]store.ChunkResult = map[string]store.ChunkResult{}
	for _, q := range queries {
		res, err := s.SearchChunks(q, limit*2)
		if err != nil {
			continue
		}
		for i, r := range res {
			scores[r.ChunkID] += 1.0 / (rrfK + float64(i+1))
			byID[r.ChunkID] = r
		}
	}
	out := make([]store.ChunkResult, 0, len(scores))
	for id := range scores {
		r := byID[id]
		r.BM25Score = scores[id]
		out = append(out, r)
	}
	// Sort desc by fused score.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].BM25Score > out[i].BM25Score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, nil
}

func (s *chunkStore) scanChunkResults(sqlText string, args ...any) ([]store.ChunkResult, error) {
	rows, err := s.b.pool.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ChunkResult
	for rows.Next() {
		var r store.ChunkResult
		var heading, content sql.NullString
		if err := rows.Scan(&r.ChunkID, &r.DocID, &heading, &content); err != nil {
			return nil, err
		}
		r.Heading, r.Content = heading.String, content.String
		r.Rank = len(out) + 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *chunkStore) Count() (int, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM chunks_meta").Scan(&n)
	return n, err
}

// NeedsBackfill: entries>0 ∧ chunks==0 (swallows errors — chunks.go:193 parity).
func (s *chunkStore) NeedsBackfill(memStore store.Countable) bool {
	ec, err := memStore.Count()
	if err != nil || ec == 0 {
		return false
	}
	cc, err := s.Count()
	if err != nil {
		return false
	}
	return cc == 0
}

func (s *chunkStore) ListAll() ([]store.ChunkEntryWithDoc, error) {
	rows, err := s.b.pool.Query(
		"SELECT chunk_id, doc_id, chunk_index, heading, content, start_offset, end_offset FROM chunks_meta ORDER BY doc_id, chunk_index")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ChunkEntryWithDoc
	for rows.Next() {
		var c store.ChunkEntryWithDoc
		var heading sql.NullString
		var so, eo sql.NullInt64
		if err := rows.Scan(&c.ChunkID, &c.DocID, &c.ChunkIndex, &heading, &c.Content, &so, &eo); err != nil {
			return nil, err
		}
		c.Heading = heading.String
		c.StartOffset, c.EndOffset = int(so.Int64), int(eo.Int64)
		out = append(out, c)
	}
	return out, rows.Err()
}
