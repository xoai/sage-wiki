package postgres

import (
	"database/sql"
	"fmt"
	"strings"

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

func (s *entryStore) Search(query string, tags []string, limit int) ([]store.SearchResult, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	where, args, next := s.b.ftsQuery("tsv", terms, 1)
	tagFrag, tagArgs, next := tagFilter("tags", tags, next)
	args = append(args, tagArgs...)
	rank, rankArgs, next := s.b.ftsRank("tsv", terms, next)
	args = append(args, rankArgs...)

	sqlText := fmt.Sprintf(
		"SELECT id, content, tags, article_path FROM entries WHERE %s%s ORDER BY %s LIMIT $%d",
		where, tagFrag, rank, next)
	args = append(args, limit)

	rows, err := s.b.pool.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.SearchResult
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, store.SearchResult{
			ID: e.ID, Content: e.Content, Tags: e.Tags, ArticlePath: e.ArticlePath,
			Rank: len(out) + 1,
		})
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
// RIGHT(id, length(id)-4) == SUBSTR(id, 5); error→0 tolerance preserved.
func (s *entryStore) CountUncompiled(query string) (int, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return 0, nil
	}
	where, args, _ := s.b.ftsQuery("e.tsv", terms, 1)
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
