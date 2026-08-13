package linter

import (
	"database/sql"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

const (
	maxLearnings = 500
	learningTTL  = 180 * 24 * time.Hour // 180 days
)

// StoreLearning saves a learning entry with deduplication.
// LearningID generates a deterministic ID for a learning entry.
// The algorithm lives in internal/store (P2-1: single home shared by both
// storage backends); this delegates for backward compatibility.
func LearningID(content string) string {
	return store.LearningID(content)
}

func StoreLearning(db store.DBHandle, learnType string, content string, tags string, lintPass string) error {
	id := LearningID(content)

	return db.WriteTx(func(tx *sql.Tx) error {
		// Dedup by content hash
		var exists int
		tx.QueryRow("SELECT COUNT(*) FROM learnings WHERE id=?", id).Scan(&exists)
		if exists > 0 {
			return nil // duplicate
		}

		_, err := tx.Exec(
			"INSERT INTO learnings (id, type, content, tags, created_at, source_lint_pass) VALUES (?, ?, ?, ?, ?, ?)",
			id, learnType, content, tags, time.Now().UTC().Format(time.RFC3339), lintPass,
		)
		return err
	})
}

// PruneLearnings enforces the 500-entry limit and 180-day TTL.
func PruneLearnings(db store.DBHandle) (int, error) {
	pruned := 0

	// TTL pruning
	err := db.WriteTx(func(tx *sql.Tx) error {
		cutoff := time.Now().Add(-learningTTL).UTC().Format(time.RFC3339)
		result, err := tx.Exec("DELETE FROM learnings WHERE created_at < ?", cutoff)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		pruned += int(n)
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Count pruning (keep newest maxLearnings)
	err = db.WriteTx(func(tx *sql.Tx) error {
		var count int
		tx.QueryRow("SELECT COUNT(*) FROM learnings").Scan(&count)
		if count <= maxLearnings {
			return nil
		}

		excess := count - maxLearnings
		result, err := tx.Exec(
			"DELETE FROM learnings WHERE id IN (SELECT id FROM learnings ORDER BY created_at ASC LIMIT ?)",
			excess,
		)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		pruned += int(n)
		return nil
	})

	if pruned > 0 {
		log.Info("learnings pruned", "count", pruned)
	}

	return pruned, err
}

// ListLearnings returns all learning entries.
func ListLearnings(db store.DBHandle) ([]Learning, error) {
	rows, err := db.ReadDB().Query(
		"SELECT id, type, content, COALESCE(tags,''), created_at, COALESCE(source_lint_pass,'') FROM learnings ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var learnings []Learning
	for rows.Next() {
		var l Learning
		if err := rows.Scan(&l.ID, &l.Type, &l.Content, &l.Tags, &l.CreatedAt, &l.SourcePass); err != nil {
			return nil, err
		}
		learnings = append(learnings, l)
	}
	return learnings, rows.Err()
}

// Learning represents a stored learning entry.
type Learning struct {
	ID         string
	Type       string
	Content    string
	Tags       string
	CreatedAt  string
	SourcePass string
}

// RecallLearnings retrieves relevant learnings for a domain query.
func RecallLearnings(db store.DBHandle, query string, limit int) ([]Learning, error) {
	if limit <= 0 {
		limit = 10
	}

	// Use FTS-like matching on content
	rows, err := db.ReadDB().Query(
		"SELECT id, type, content, COALESCE(tags,''), created_at, COALESCE(source_lint_pass,'') FROM learnings WHERE content LIKE ? OR tags LIKE ? ORDER BY created_at DESC LIMIT ?",
		"%"+query+"%", "%"+query+"%", limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var learnings []Learning
	for rows.Next() {
		var l Learning
		rows.Scan(&l.ID, &l.Type, &l.Content, &l.Tags, &l.CreatedAt, &l.SourcePass)
		learnings = append(learnings, l)
	}
	return learnings, rows.Err()
}
