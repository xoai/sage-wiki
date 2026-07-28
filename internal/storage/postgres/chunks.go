package postgres

import (
	"database/sql"
	"fmt"
	"strings"

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
	limit = normLimit(limit, 20)
	// Probes the DOCUMENT corpus, matching the sqlite twin: identical
	// doc-ratio semantics across both fusion legs by construction, and a
	// far cheaper probe than counting distinct docs among chunk rows.
	terms := s.b.dfPruneTerms(
		"SELECT count(*) FROM entries",
		"SELECT count(*) FROM entries WHERE tsv @@ to_tsquery('sage_fts', $1)",
		queryTerms(query))
	if len(terms) == 0 {
		return nil, nil
	}
	plan := s.b.planFTS("tsv", "coalesce(heading,'') || ' ' || content", terms, 1)
	args := append(plan.args, limit)
	rankSel := "0.0"
	if strings.HasPrefix(plan.rank, "ts_rank(") {
		rankSel = strings.TrimSuffix(plan.rank, " DESC")
	}
	sqlText := fmt.Sprintf(
		"SELECT chunk_id, doc_id, heading, content, %s FROM chunks_meta WHERE %s ORDER BY %s LIMIT $%d",
		rankSel, plan.where, plan.rank, plan.next)
	return s.scanChunkResults(sqlText, args...)
}

// GetChunksMeta returns heading and content for the given chunk IDs —
// the pg twin of the sqlite hydration read. Missing IDs are absent.
func (s *chunkStore) GetChunksMeta(ids []string) (map[string]store.ChunkEntry, error) {
	if len(ids) == 0 {
		return map[string]store.ChunkEntry{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := s.b.pool.Query(
		"SELECT chunk_id, chunk_index, heading, content FROM chunks_meta WHERE chunk_id IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("pg chunks.GetChunksMeta: %w", err)
	}
	defer rows.Close()

	out := make(map[string]store.ChunkEntry, len(ids))
	for rows.Next() {
		var c store.ChunkEntry
		var heading, content sql.NullString
		if err := rows.Scan(&c.ChunkID, &c.ChunkIndex, &heading, &content); err != nil {
			return nil, fmt.Errorf("pg chunks.GetChunksMeta scan: %w", err)
		}
		c.Heading, c.Content = heading.String, content.String
		out[c.ChunkID] = c
	}
	return out, rows.Err()
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
		if err := rows.Scan(&r.ChunkID, &r.DocID, &heading, &content, &r.BM25Score); err != nil {
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

// ListDocIDs — pg twin: distinct doc IDs that currently have chunks.
func (s *chunkStore) ListDocIDs() ([]string, error) {
	rows, err := s.b.pool.Query("SELECT DISTINCT doc_id FROM chunks_meta ORDER BY doc_id")
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
