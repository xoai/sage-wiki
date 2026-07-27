package postgres

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
		// A suppressed write here is the designed no-op: the guard exists so an
		// auto-applied re-proposal cannot overwrite a human's rejection.
		_, err := putAliasTx(tx, a)
		return err
	})
}

// putAliasTx is the single alias-upsert statement, shared by PutAlias and
// LinkAlias so the audit row is written exactly one way. LinkAlias cannot call
// PutAlias: WriteTx's mutex is not reentrant, so a nested call would deadlock.
func putAliasTx(tx *sql.Tx, a store.EntityAlias) (written bool, err error) {
	res, err := tx.Exec(
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
		a.Reason, a.Source, a.CreatedAt,
		nullStr(a.DecidedAt), nullStr(a.DecidedBy),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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
		res, err := tx.Exec(
			`UPDATE entity_aliases SET status=$3, decided_at=$4, decided_by=$5
			 WHERE alias=$1 AND canonical_id=$2`,
			alias, canonicalID, string(status), now, decidedBy)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("ontology: no alias row %q -> %q", alias, canonicalID)
		}
		return nil
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

// copiedRelationID — see internal/ontology/aliases.go. Eight BYTES of hex,
// matching tripleRelationID; the "alias:" prefix marks a DERIVED edge whose
// evidence quotes the alias's source document, not the canonical's.
func copiedRelationID(source, predicate, target string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + predicate + "\x00" + target))
	return "alias:" + hex.EncodeToString(sum[:8])
}

// LinkAlias copies the alias's edges onto the canonical and records the link in
// one transaction. Non-destructive: nothing is deleted or overwritten.
//
// The statements are written per-backend rather than shared with the sqlite
// half: placeholders differ ($N vs ?), and relations.created_at is TIMESTAMPTZ
// here (bound through nullRFC) but TEXT there.
func (s *ontologyStore) LinkAlias(a store.EntityAlias) (store.LinkResult, error) {
	var res store.LinkResult
	if a.Alias == a.CanonicalID {
		return res, fmt.Errorf("ontology: entity %q cannot alias itself", a.Alias)
	}

	var derivedWritten bool
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		// A missing endpoint is a fact to report, not an error: --prune and
		// reconcile delete entities without consulting this table. Neither
		// branch writes an alias row.
		exists := func(id string) (bool, error) {
			var n int
			err := tx.QueryRow("SELECT COUNT(*) FROM entities WHERE id=$1", id).Scan(&n)
			return n > 0, err
		}
		aliasOK, err := exists(a.Alias)
		if err != nil {
			return err
		}
		if !aliasOK {
			res.AliasMissing = true
			return nil
		}
		canonOK, err := exists(a.CanonicalID)
		if err != nil {
			return err
		}
		if !canonOK {
			res.CanonicalMissing = true
			return nil
		}

		// Scoped so the cursor is released before the INSERT loop below runs on
		// the same transaction, while still closing via defer.
		//
		// This read UNIONS derived rows, and that is load-bearing: it is what
		// makes A->B->C chains converge. Reading `relations` alone after
		// decision-035 would make A's contribution invisible to LinkAlias(B->C).
		edges, err := func() ([]store.Relation, error) {
			q := "SELECT " + relationCols + " FROM relations WHERE source_id=$1 OR target_id=$1"
			if s.b.derivedExists() {
				q += "\nUNION ALL\nSELECT " + relationCols + " FROM derived_relations d" +
					" WHERE (d.source_id=$1 OR d.target_id=$1)" + derivedNotShadowed
			}
			rows, err := tx.Query(q, a.Alias)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			return scanRelations(rows)
		}()
		if err != nil {
			return err
		}

		for _, r := range edges {
			src, tgt := r.SourceID, r.TargetID
			if src == a.Alias {
				src = a.CanonicalID
			}
			if tgt == a.Alias {
				tgt = a.CanonicalID
			}
			if src == tgt {
				res.SelfLoops++
				continue
			}

			// Derived edges live in their own table stamped with their cause
			// (decision-035). The lookup does two jobs: a GENUINE original must
			// never be displaced (Skipped, as the old ON CONFLICT protected),
			// while a P3-3 ANONYMOUS COPY — identifiable because its id is
			// exactly copiedRelationID for these endpoints — is converted in
			// place. Exact id, no LIKE, so an entity id merely starting with
			// "alias:" is never at risk.
			wantID := copiedRelationID(src, r.Relation, tgt)
			var existingID sql.NullString
			err := tx.QueryRow(
				`SELECT id FROM relations WHERE source_id=$1 AND target_id=$2 AND relation=$3`,
				src, tgt, r.Relation).Scan(&existingID)
			switch {
			case err == sql.ErrNoRows:
				// nothing there; fall through to the insert
			case err != nil:
				return err
			case existingID.Valid && existingID.String == wantID:
				if _, err := tx.Exec(`DELETE FROM relations WHERE id=$1`, wantID); err != nil {
					return err
				}
				res.Converted++
			default:
				res.Skipped++
				continue
			}

			out, err := tx.Exec(`
				INSERT INTO derived_relations (alias_id, id, source_id, target_id, relation,
				                               created_at, evidence, confidence, source_doc,
				                               valid_from, valid_to, invalidated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (alias_id, source_id, target_id, relation) DO NOTHING`,
				a.Alias, wantID, src, tgt, r.Relation, nullRFC(r.CreatedAt),
				nullStr(r.Evidence), r.Confidence, nullStr(r.SourceDoc),
				nullStr(r.ValidFrom), nullStr(r.ValidTo), nullStr(r.InvalidatedBy))
			if err != nil {
				return err
			}
			n, err := out.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				res.Copied++
				derivedWritten = true
			} else {
				res.Skipped++
			}
		}

		written, err := putAliasTx(tx, a)
		if err != nil {
			return err
		}
		if !written {
			return fmt.Errorf(
				"ontology: refusing to link %q -> %q: the pair is rejected, so the link cannot be recorded",
				a.Alias, a.CanonicalID)
		}
		return nil
	})
	if err != nil {
		return store.LinkResult{}, err
	}
	if derivedWritten {
		s.b.markDerivedWritten()
	}
	return res, nil
}

// UnlinkAlias reverses a link. See store.OntologyStore for the contract.
func (s *ontologyStore) UnlinkAlias(alias, canonicalID string) error {
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM derived_relations WHERE alias_id=$1`, alias); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE entity_aliases SET status='rejected', decided_at=$1, decided_by='unlink'
			  WHERE alias=$2 AND canonical_id=$3`,
			time.Now().UTC().Format(time.RFC3339), alias, canonicalID)
		return err
	})
	if err != nil {
		return err
	}
	s.b.markDerivedMaybeEmpty()
	return nil
}

// ClearDerived removes every derived edge, for the sweep's rebuild pass.
func (s *ontologyStore) ClearDerived() error {
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM derived_relations`)
		return err
	})
	if err != nil {
		return err
	}
	s.b.markDerivedMaybeEmpty()
	return nil
}
