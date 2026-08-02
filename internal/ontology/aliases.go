package ontology

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
		a.CreatedAt = s.nowUTC().Format(time.RFC3339)
	}
	if a.Source == "" {
		a.Source = "llm"
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		// A suppressed write here is the designed no-op: the guard exists so an
		// auto-applied re-proposal cannot overwrite a human's rejection.
		_, err := putAliasTx(tx, a)
		return err
	})
}

// putAliasTx is the single alias-upsert statement, shared by PutAlias and
// LinkAlias so the audit row is written exactly one way. LinkAlias cannot call
// PutAlias: WriteTx's mutex is not reentrant (storage/db.go), so a nested call
// would deadlock.
func putAliasTx(tx *sql.Tx, a store.EntityAlias) (written bool, err error) {
	res, err := tx.Exec(
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
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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
	now := s.nowUTC().Format(time.RFC3339)
	return s.db.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE entity_aliases SET status=?, decided_at=?, decided_by=?
			 WHERE alias=? AND canonical_id=?`,
			string(status), now, decidedBy, alias, canonicalID)
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

// copiedRelationID derives the id for an edge copied onto a canonical.
//
// A fresh id is required: the source row survives (linking deletes nothing) and
// still owns its own id. The "alias:" prefix marks the edge as DERIVED — its
// evidence quotes the alias's source document, not the canonical's — and
// guarantees no collision with a keyword id ("source-relation-target") or a
// triple id ("triple:...").
//
// Eight BYTES of hex, matching tripleRelationID. A 32-bit id would collide by
// birthday around 65k copied edges, and a colliding id carrying a different
// (source_id, target_id, relation) is a NON-TARGET unique violation that the
// ON CONFLICT clause below does not absorb — the edge would be lost.
func copiedRelationID(source, predicate, target string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + predicate + "\x00" + target))
	return "alias:" + hex.EncodeToString(sum[:8])
}

// LinkAlias copies the alias's edges onto the canonical and records the link,
// in one transaction. It is NON-DESTRUCTIVE: nothing is deleted and nothing
// existing is overwritten.
//
// This is deliberately weaker than the upstream spec's "collapse into one
// entity". Deleting the absorbed row was tried and abandoned across three spec
// revisions: the row that owns an article and the row that owns a description
// are different rows with different ids (write.go writes ID: concept.Name with
// an article and no definition; the triples pass writes the model's raw string
// with a definition and no article), and they are exactly the pair resolution
// targets — so deleting either loses something no audit trail could restore.
// Deletion also raced --prune, reconcile and the manifest, all of which
// re-create entities independently.
//
// It does NOT chain-resolve the canonical. Callers resolve first; resolving
// here would let an --apply write a row for a different (alias, canonical_id)
// pair than the pending one it came from, leaving two ACTIVE rows for one alias
// and violating the partial unique index. Chains converge instead: the sweep
// replays every applied row each pass, so A->B->C settles within one extra pass.
func (s *Store) LinkAlias(a store.EntityAlias) (store.LinkResult, error) {
	var res store.LinkResult
	if a.Alias == a.CanonicalID {
		return res, fmt.Errorf("ontology: entity %q cannot alias itself", a.Alias)
	}

	var derivedWritten bool
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		// Both endpoints must exist. A missing endpoint is a FACT to report, not
		// an error: --prune and reconcile delete entities without consulting this
		// table, so the sweep meets this routinely and must not fail a compile.
		// Neither branch writes an alias row — recording a link that did not
		// happen would make the audit trail lie.
		exists := func(id string) (bool, error) {
			var n int
			err := tx.QueryRow("SELECT COUNT(*) FROM entities WHERE id=?", id).Scan(&n)
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
		// makes A->B->C chains converge (see the doc comment above). Once copies
		// live in derived_relations, reading `relations` alone would make A's
		// contribution invisible to LinkAlias(B->C) and the chain would never
		// complete.
		edges, err := func() ([]store.Relation, error) {
			q := `SELECT ` + relationCols + ` FROM relations WHERE source_id=? OR target_id=?`
			args := []any{a.Alias, a.Alias}
			if s.derivedExists() {
				q += "\nUNION ALL\nSELECT " + relationCols + " FROM derived_relations d" +
					" WHERE (d.source_id=? OR d.target_id=?)" + derivedNotShadowed
				args = append(args, a.Alias, a.Alias)
			}
			rows, err := tx.Query(q, args...)
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
			// The other endpoint IS the canonical, so the copy would be a
			// self-loop. Skip the COPY; the original edge stays where it is.
			if src == tgt {
				res.SelfLoops++
				continue
			}

			// Derived edges live in their own table, stamped with the alias
			// that caused them, so un-link is a delete by cause rather than a
			// reconstruction (decision-035).
			//
			// The lookup below does two jobs at once. If `relations` already
			// holds this edge it is either:
			//
			//   - a GENUINE original the canonical asserted itself, which a
			//     derived row must never displace — that is what the old
			//     ON CONFLICT DO NOTHING protected, and Skipped still counts it;
			//   - or a P3-3 ANONYMOUS COPY, identifiable because its id is
			//     exactly copiedRelationID for these endpoints. That one is
			//     converted in place: deleted here and re-inserted below with
			//     its cause recorded. No LIKE predicate is involved, so an
			//     entity id that merely starts with "alias:" is never at risk.
			wantID := copiedRelationID(src, r.Relation, tgt)
			var existingID sql.NullString
			err := tx.QueryRow(
				`SELECT id FROM relations WHERE source_id=? AND target_id=? AND relation=?`,
				src, tgt, r.Relation).Scan(&existingID)
			switch {
			case err == sql.ErrNoRows:
				// nothing there; fall through to the insert
			case err != nil:
				return err
			case existingID.Valid && existingID.String == wantID:
				if _, err := tx.Exec(`DELETE FROM relations WHERE id=?`, wantID); err != nil {
					return err
				}
				res.Converted++
			default:
				res.Skipped++
				continue
			}

			out, err := tx.Exec(
				`INSERT INTO derived_relations (alias_id, id, source_id, target_id, relation,
				                                created_at, evidence, confidence, source_doc,
				                                valid_from, valid_to, invalidated_by)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(alias_id, source_id, target_id, relation) DO NOTHING`,
				a.Alias, wantID, src, tgt, r.Relation, r.CreatedAt,
				r.Evidence, r.Confidence, r.SourceDoc,
				r.ValidFrom, r.ValidTo, r.InvalidatedBy)
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

		// The audit row lands in the SAME transaction as the copies — a
		// caller-side second write could fail after the copies committed.
		//
		// A SUPPRESSED write (the rejected-row guard) must abort: copying edges
		// with nothing recording the link mutates the graph invisibly, and the
		// alias would be re-seeded and re-copied on every later run. Returning
		// an error rolls the copies back with it.
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
		s.markDerivedWritten()
	}
	return res, nil
}

// UnlinkAlias reverses a link. See store.OntologyStore for the contract.
func (s *Store) UnlinkAlias(alias, canonicalID string) error {
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM derived_relations WHERE alias_id=?`, alias); err != nil {
			return err
		}
		// Rejection, not deletion of the alias row: the audit trail is what
		// keeps the pair from being re-proposed, and putAliasTx's rejected-row
		// guard depends on it existing.
		_, err := tx.Exec(
			`UPDATE entity_aliases SET status='rejected', decided_at=?, decided_by='unlink'
			  WHERE alias=? AND canonical_id=?`,
			s.nowUTC().Format(time.RFC3339), alias, canonicalID)
		return err
	})
	if err != nil {
		return err
	}
	s.markDerivedMaybeEmpty()
	return nil
}

// ClearDerived removes every derived edge, for the sweep's rebuild pass.
func (s *Store) ClearDerived() error {
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM derived_relations`)
		return err
	})
	if err != nil {
		return err
	}
	s.markDerivedMaybeEmpty()
	return nil
}
