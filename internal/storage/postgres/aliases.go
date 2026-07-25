package postgres

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// Entity resolution storage (P3-3, GRAPH-03) — the Postgres half of
// internal/ontology/aliases.go. Statements are written per-backend rather than
// shared: placeholders differ ($N vs ?), and the audit timestamps are TEXT here
// on purpose (see below), unlike every other timestamp in this package.

// aliasCols mirrors the sqlite column list. entity_aliases.created_at and
// decided_at are TEXT on BOTH backends and are bound as raw strings — NOT
// through nullRFC/scanNullRFC, which round-trip via time.Time and would store
// Postgres's own rendering, breaking byte parity with the RFC3339 string SQLite
// keeps. Same reasoning P3-1 applied to relations.valid_from.
const aliasCols = `alias, canonical_id, entity_type, status,
	COALESCE(confidence,0), COALESCE(reason,''), source,
	created_at, COALESCE(decided_at,''), COALESCE(decided_by,'')`

const maxAliasChain = 32

func scanAliasRows(rows *sql.Rows) ([]store.EntityAlias, error) {
	var out []store.EntityAlias
	for rows.Next() {
		var a store.EntityAlias
		if err := rows.Scan(
			&a.Alias, &a.CanonicalID, &a.EntityType, &a.Status,
			&a.Confidence, &a.Reason, &a.Source,
			&a.CreatedAt, &a.DecidedAt, &a.DecidedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PutAlias — see internal/ontology/aliases.go for the full rationale. The SET
// list excludes created_at/source/entity_type so the sweep cannot rewrite the
// audit trail's origin, and the WHERE stops an auto-apply from overwriting a
// human's rejection.
func (s *ontologyStore) PutAlias(a store.EntityAlias) error {
	if a.Alias == "" || a.CanonicalID == "" {
		return fmt.Errorf("ontology: alias and canonical_id are required")
	}
	if a.Alias == a.CanonicalID {
		return fmt.Errorf("ontology: entity %q cannot alias itself", a.Alias)
	}
	if a.CreatedAt == "" {
		a.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if a.Source == "" {
		a.Source = "llm"
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO entity_aliases
			   (alias, canonical_id, entity_type, status, confidence, reason,
			    source, created_at, decided_at, decided_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (alias, canonical_id) DO UPDATE SET
			   status     = excluded.status,
			   confidence = excluded.confidence,
			   reason     = excluded.reason,
			   decided_at = excluded.decided_at,
			   decided_by = excluded.decided_by
			 WHERE entity_aliases.status <> 'rejected'`,
			a.Alias, a.CanonicalID, a.EntityType, string(a.Status), a.Confidence,
			nullStr(a.Reason), a.Source, a.CreatedAt,
			nullStr(a.DecidedAt), nullStr(a.DecidedBy),
		)
		return err
	})
}

// GetActiveAlias returns the live ('applied' or 'pending') decision, or
// (nil, nil). Rejected rows are not active — a rejection means NOT linked.
func (s *ontologyStore) GetActiveAlias(alias string) (*store.EntityAlias, error) {
	row := s.b.ReadDB().QueryRow(
		`SELECT `+aliasCols+` FROM entity_aliases
		 WHERE alias=$1 AND status IN ('applied','pending')`, alias)
	var a store.EntityAlias
	if err := row.Scan(
		&a.Alias, &a.CanonicalID, &a.EntityType, &a.Status,
		&a.Confidence, &a.Reason, &a.Source,
		&a.CreatedAt, &a.DecidedAt, &a.DecidedBy,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// ListAliases is ordered so the sweep replays rows deterministically — an
// unordered scan makes the outcome depend on seq-scan order, which can differ
// between runs and between backends.
func (s *ontologyStore) ListAliases(status store.AliasStatus) ([]store.EntityAlias, error) {
	rows, err := s.b.ReadDB().Query(
		`SELECT `+aliasCols+` FROM entity_aliases WHERE status=$1
		 ORDER BY alias, canonical_id`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAliasRows(rows)
}

// IsRejected matches the pair in EITHER direction — a rejection says the two
// entities differ, which does not depend on which was nominated canonical.
func (s *ontologyStore) IsRejected(a, b string) (bool, error) {
	var n int
	err := s.b.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM entity_aliases
		 WHERE status='rejected'
		   AND ((alias=$1 AND canonical_id=$2) OR (alias=$2 AND canonical_id=$1))`,
		a, b).Scan(&n)
	return n > 0, err
}

func (s *ontologyStore) SetAliasStatus(alias, canonicalID string, status store.AliasStatus, decidedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE entity_aliases SET status=$3, decided_at=$4, decided_by=$5
			 WHERE alias=$1 AND canonical_id=$2`,
			alias, canonicalID, string(status), now, decidedBy)
		return err
	})
}

// CanonicalID follows APPLIED rows only; a pending row is an un-approved
// proposal. Cycles return the input id and log rather than looping.
func (s *ontologyStore) CanonicalID(id string) (string, error) {
	seen := map[string]bool{id: true}
	cur := id
	for i := 0; i < maxAliasChain; i++ {
		var next string
		err := s.b.ReadDB().QueryRow(
			`SELECT canonical_id FROM entity_aliases WHERE alias=$1 AND status='applied'`,
			cur).Scan(&next)
		if err == sql.ErrNoRows {
			return cur, nil
		}
		if err != nil {
			return "", err
		}
		if seen[next] {
			log.Warn("ontology: alias cycle detected, resolving to the input id",
				"id", id, "at", cur, "next", next)
			return id, nil
		}
		seen[next] = true
		cur = next
	}
	log.Warn("ontology: alias chain exceeded the hop cap", "id", id, "cap", maxAliasChain)
	return cur, nil
}
