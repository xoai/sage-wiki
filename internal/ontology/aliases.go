package ontology

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// Entity resolution storage (P3-3, GRAPH-03).
//
// Alias rows key on entity IDs, never names: Entity.ID != Entity.Name for every
// entity the compiler writes (write.go writes ID: concept.Name with
// Name: FormatConceptName(...)), and two rows can legally share a Name.

// aliasCols is the single column list for every alias read path, mirroring
// relationCols' rationale from P3-1: keeping several copies in sync by hand is
// only a matter of time. Every SELECT using it must scan via scanAlias.
const aliasCols = `alias, canonical_id, entity_type, status,
	COALESCE(confidence,0), COALESCE(reason,''), source,
	created_at, COALESCE(decided_at,''), COALESCE(decided_by,'')`

func scanAliases(rows *sql.Rows) ([]store.EntityAlias, error) {
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

// maxAliasChain bounds CanonicalID's walk. A cycle is only reachable through
// manual edits or a bug, but an unbounded walk inside a compile pass is not an
// acceptable failure mode, so the walk is bounded as well as visited-guarded.
const maxAliasChain = 32

// PutAlias inserts or updates one alias decision.
//
// The SET list is explicit and deliberately EXCLUDES created_at, source and
// entity_type. The re-link sweep runs this against every applied row on every
// compile; if the origin fields were updatable, the audit trail's timestamp and
// provenance would be rewritten each run and could no longer explain the link
// after the fact.
//
// The WHERE clause is the other half: without it, an auto-applied re-proposal
// would flip a human's 'rejected' row to 'applied' and destroy the record of
// the decision. A no-op is the correct outcome there, not an error — the caller
// re-checks IsRejected before proposing, and this is the backstop.
func (s *Store) PutAlias(a store.EntityAlias) error {
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
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO entity_aliases
			   (alias, canonical_id, entity_type, status, confidence, reason,
			    source, created_at, decided_at, decided_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (alias, canonical_id) DO UPDATE SET
			   status     = excluded.status,
			   confidence = excluded.confidence,
			   reason     = excluded.reason,
			   decided_at = excluded.decided_at,
			   decided_by = excluded.decided_by
			 WHERE entity_aliases.status <> 'rejected'`,
			a.Alias, a.CanonicalID, a.EntityType, string(a.Status), a.Confidence,
			a.Reason, a.Source, a.CreatedAt, nullText(a.DecidedAt), nullText(a.DecidedBy),
		)
		return err
	})
}

// nullText keeps "" out of the nullable audit columns so a never-decided row
// reads back as NULL rather than an empty string that looks like a decision.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetActiveAlias returns the live decision for an alias — 'applied' or
// 'pending' — or (nil, nil) when there is none, matching GetEntity's contract.
//
// Rejected rows are deliberately NOT active: a rejection means the pair is not
// linked, and surfacing it here would make callers treat it as a live link.
// The partial unique index guarantees at most one row can match.
func (s *Store) GetActiveAlias(alias string) (*store.EntityAlias, error) {
	row := s.db.ReadDB().QueryRow(
		`SELECT `+aliasCols+` FROM entity_aliases
		 WHERE alias=? AND status IN ('applied','pending')`, alias)
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

// ListAliases returns every row with the given status, ordered by
// (alias, canonical_id).
//
// The ordering is load-bearing, not cosmetic: the sweep replays applied rows in
// this order, and an unordered scan makes the outcome depend on table layout —
// which differs between SQLite and Postgres and can differ between runs.
func (s *Store) ListAliases(status store.AliasStatus) ([]store.EntityAlias, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT `+aliasCols+` FROM entity_aliases WHERE status=?
		 ORDER BY alias, canonical_id`, string(status))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAliases(rows)
}

// IsRejected reports whether this PAIR was rejected, in either direction.
//
// Symmetry is the point. A rejection is a judgement that two entities are
// different, which does not depend on which one a model nominated as canonical.
// A direction-keyed check would let the next run re-propose the same pair with
// the endpoints swapped and link it at high confidence — silently reversing the
// user's decision.
func (s *Store) IsRejected(a, b string) (bool, error) {
	var n int
	err := s.db.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM entity_aliases
		 WHERE status='rejected'
		   AND ((alias=? AND canonical_id=?) OR (alias=? AND canonical_id=?))`,
		a, b, b, a).Scan(&n)
	return n > 0, err
}

// SetAliasStatus moves one row to a new status, stamping the decision.
func (s *Store) SetAliasStatus(alias, canonicalID string, status store.AliasStatus, decidedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE entity_aliases SET status=?, decided_at=?, decided_by=?
			 WHERE alias=? AND canonical_id=?`,
			string(status), now, decidedBy, alias, canonicalID)
		return err
	})
}

// CanonicalID walks the applied-alias chain to its terminal entity.
//
// Only 'applied' rows are followed. A 'pending' row is an UN-APPROVED proposal:
// following it would resolve an entity into one a human never sanctioned, and
// one it was never compared against.
//
// A cycle returns the input id and logs, rather than looping. Read-only and
// outside any transaction — LinkAlias uses the tx-scoped variant instead,
// because WriteTx's mutex is not reentrant.
func (s *Store) CanonicalID(id string) (string, error) {
	seen := map[string]bool{id: true}
	cur := id
	for i := 0; i < maxAliasChain; i++ {
		var next string
		err := s.db.ReadDB().QueryRow(
			`SELECT canonical_id FROM entity_aliases WHERE alias=? AND status='applied'`,
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
