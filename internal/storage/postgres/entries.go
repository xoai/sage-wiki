package postgres

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/store"
)

type entryStore struct{ b *backend }

var _ store.EntryStore = (*entryStore)(nil)

func (s *entryStore) Add(e store.Entry) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO entries (id, content, tags, article_path) VALUES ($1, $2, $3, $4)",
			e.ID, e.Content, strings.Join(e.Tags, ","), e.ArticlePath)
		return err
	})
}

func (s *entryStore) Update(e store.Entry) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE entries SET content=$2, tags=$3, article_path=$4 WHERE id=$1",
			e.ID, e.Content, strings.Join(e.Tags, ","), e.ArticlePath)
		return err
	})
}

func (s *entryStore) Delete(id string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entries WHERE id=$1", id)
		return err
	})
}

func scanEntry(row interface{ Scan(...any) error }) (*store.Entry, error) {
	var e store.Entry
	var tags, content, ap sql.NullString
	var id sql.NullString
	if err := row.Scan(&id, &content, &tags, &ap); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.ID, e.Content, e.ArticlePath = id.String, content.String, ap.String
	if tags.String != "" {
		e.Tags = strings.Split(tags.String, ",")
	}
	return &e, nil
}

func (s *entryStore) Get(id string) (*store.Entry, error) {
	return scanEntry(s.b.pool.QueryRow(
		"SELECT id, content, tags, article_path FROM entries WHERE id=$1", id))
}

// GetMany — pg twin of the batched entry hydration (M5). Missing IDs are
// absent from the map; batched to stay under bind limits.
func (s *entryStore) GetMany(ids []string) (map[string]*store.Entry, error) {
	const batchSize = 500
	out := make(map[string]*store.Entry, len(ids))
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			args[i] = id
		}
		rows, err := s.b.pool.Query(
			"SELECT id, content, tags, article_path FROM entries WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return nil, fmt.Errorf("pg entries.GetMany: %w", err)
		}
		for rows.Next() {
			var id, content, tags, ap sql.NullString
			if err := rows.Scan(&id, &content, &tags, &ap); err != nil {
				rows.Close()
				return nil, err
			}
			e := &store.Entry{ID: id.String, Content: content.String, ArticlePath: ap.String}
			if tags.String != "" {
				e.Tags = strings.Split(tags.String, ",")
			}
			out[e.ID] = e
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// SetSourceDate — pg twin of the sqlite sidecar upsert (ADR-039).
func (s *entryStore) SetSourceDate(id string, ts int64) error {
	if ts <= 0 {
		return nil
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO entry_dates (id, source_date) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET source_date = excluded.source_date",
			id, ts)
		return err
	})
}

// GetSourceDates — pg twin; missing IDs absent from the map. Batched to
// stay under bind limits (memory.sourceDateBatch parity, F-067).
func (s *entryStore) GetSourceDates(ids []string) (map[string]int64, error) {
	const batchSize = 500
	out := make(map[string]int64, len(ids))
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			args[i] = id
		}
		rows, err := s.b.pool.Query(
			"SELECT id, source_date FROM entry_dates WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return nil, fmt.Errorf("pg entries.GetSourceDates: %w", err)
		}
		for rows.Next() {
			var id string
			var ts int64
			if err := rows.Scan(&id, &ts); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = ts
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func (s *entryStore) Search(query string, tags []string, limit int) ([]store.SearchResult, error) {
	if limit <= 0 {
		limit = 10 // memory/entries.go:84-86 parity
	}
	terms := s.b.dfPruneTerms(
		"SELECT count(*) FROM entries",
		"SELECT count(*) FROM entries WHERE tsv @@ to_tsquery('sage_fts', $1)",
		queryTerms(query))
	if len(terms) == 0 {
		return nil, nil
	}
	plan := s.b.planFTS("tsv", "content", terms, 1)
	tagFrag, tagArgs, next := tagFilter("tags", tags, plan.next)
	args := append(plan.args, tagArgs...)

	rankSel := "0.0"
	if strings.HasPrefix(plan.rank, "ts_rank(") {
		rankSel = strings.TrimSuffix(plan.rank, " DESC")
	}
	sqlText := fmt.Sprintf(
		"SELECT id, content, tags, article_path, %s FROM entries WHERE %s%s ORDER BY %s LIMIT $%d",
		rankSel, plan.where, tagFrag, plan.rank, next)
	args = append(args, limit)

	rows, err := s.b.pool.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.SearchResult
	for rows.Next() {
		var id, tags, content, ap sql.NullString
		var score float64
		if err := rows.Scan(&id, &content, &tags, &ap, &score); err != nil {
			return nil, err
		}
		r := store.SearchResult{
			ID: id.String, Content: content.String, ArticlePath: ap.String,
			BM25Score: score, Rank: len(out) + 1,
		}
		if tags.String != "" {
			r.Tags = strings.Split(tags.String, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *entryStore) Count() (int, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM entries").Scan(&n)
	return n, err
}

func (s *entryStore) ListAll() ([]store.Entry, error) {
	rows, err := s.b.pool.Query("SELECT id, content, tags, article_path FROM entries")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// CountUncompiled mirrors the sqlite cross-store join (mcp:301 semantics):
// the RAW SanitizeFTS output is one concatenated token (no stopword
// filtering, no OR-join) matched as a prefix term; error→0 tolerance.
func (s *entryStore) CountUncompiled(query string) (int, error) {
	sanitized := memory.SanitizeFTS(query)
	if sanitized == "" {
		return 0, nil
	}
	plan := s.b.planFTS("e.tsv", "e.content", []string{sanitized}, 1)
	args := plan.args
	where := plan.where
	var count int
	err := s.b.pool.QueryRow(fmt.Sprintf(`
		SELECT COUNT(*) FROM entries e
		JOIN compile_items ci ON ci.source_path = RIGHT(e.id, length(e.id)-4)
		WHERE %s AND e.id LIKE 'src:%%' AND ci.tier < 3`, where), args...).Scan(&count)
	if err != nil {
		return 0, nil // documented tolerance (legacy vaults)
	}
	return count, nil
}
