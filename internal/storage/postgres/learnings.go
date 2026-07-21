package postgres

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

func hashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

type learningStore struct{ b *backend }

var _ store.LearningStore = (*learningStore)(nil)

// Store: dedup by server-side LearningID (spec §3 — the hash has exactly one
// home in store.LearningID; caller-supplied ID is ignored).
func (s *learningStore) Store(l store.Learning) error {
	id := store.LearningID(l.Content)
	return s.b.WriteTx(func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM learnings WHERE id=$1", id).Scan(&exists); err != nil {
			return fmt.Errorf("learnings dedup probe: %w", err)
		}
		if exists > 0 {
			return nil // duplicate
		}
		_, err := tx.Exec(
			"INSERT INTO learnings (id, type, content, tags, created_at, source_lint_pass) VALUES ($1,$2,$3,$4,$5,$6)",
			id, l.Type, l.Content, nullStr(l.Tags), time.Now().UTC(), nullStr(l.SourcePass))
		return err
	})
}

const learningCols = "id, type, content, tags, created_at, source_lint_pass"

func scanLearnings(rows *sql.Rows) ([]store.Learning, error) {
	var out []store.Learning
	for rows.Next() {
		var l store.Learning
		var tags, sp sql.NullString
		var ca *time.Time
		if err := rows.Scan(&l.ID, &l.Type, &l.Content, &tags, &ca, &sp); err != nil {
			return nil, err
		}
		l.Tags, l.SourcePass = tags.String, sp.String
		l.CreatedAt = scanNullRFC(ca)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *learningStore) List() ([]store.Learning, error) {
	rows, err := s.b.pool.Query("SELECT " + learningCols + " FROM learnings ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// Recall: sqlite LIKE is case-insensitive for ASCII → ILIKE (spec parity).
func (s *learningStore) Recall(query string, limit int) ([]store.Learning, error) {
	limit = normLimit(limit, 10)
	rows, err := s.b.pool.Query(
		"SELECT "+learningCols+" FROM learnings WHERE content ILIKE $1 OR tags ILIKE $1 ORDER BY created_at DESC LIMIT $2",
		"%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// Prune: TTL (180 days, RFC3339-UTC cutoff — chronological on TIMESTAMPTZ)
// + 500-entry cap (learning.go parity).
func (s *learningStore) Prune() (int, error) {
	pruned := 0
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		cutoff := time.Now().Add(-180 * 24 * time.Hour).UTC()
		res, err := tx.Exec("DELETE FROM learnings WHERE created_at < $1", cutoff)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		pruned += int(n)

		res, err = tx.Exec(`
			DELETE FROM learnings WHERE id NOT IN (
				SELECT id FROM learnings ORDER BY created_at DESC LIMIT 500)`)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		pruned += int(n)
		return nil
	})
	return pruned, err
}
