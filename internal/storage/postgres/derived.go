package postgres

import "time"

// Alias-derived edges (decision-035) — the Postgres half of internal/ontology's
// derived.go. Read that file first; the reasoning is the same and is not
// repeated here.
//
// Two differences from the SQLite side, both mechanical:
//
//   - placeholders are $N, so both arms can bind the SAME argument and there is
//     no need to duplicate the caller's args;
//   - the guard lives on *backend, not on the store. Ontology() returns a fresh
//     &ontologyStore{} per call (backend.go), so a per-store flag would re-probe
//     on every construction and never be shared.

// derivedNotShadowed is the two anti-joins: a derived row is suppressed when an
// original asserts the same edge, and only the lowest alias_id survives when
// several aliases derive one edge.
const derivedNotShadowed = `
   AND NOT EXISTS (SELECT 1 FROM relations r
                    WHERE r.source_id=d.source_id AND r.target_id=d.target_id
                      AND r.relation=d.relation)
   AND NOT EXISTS (SELECT 1 FROM derived_relations d2
                    WHERE d2.source_id=d.source_id AND d2.target_id=d.target_id
                      AND d2.relation=d.relation AND d2.alias_id < d.alias_id)`

// derivedArm is the second half of a relation-returning union.
func derivedArm(predicate string) string {
	return `
UNION ALL
SELECT ` + relationCols + ` FROM derived_relations d
 WHERE ` + predicate + derivedNotShadowed
}

// derivedRecheck bounds how stale a FALSE guard may be — see the SQLite twin
// for why a once-only probe was a regression rather than a limitation.
const derivedRecheck = time.Second

// derivedExists reports whether derived_relations holds any row.
//
// Fails safe in one direction: a stale true is merely slower, a stale false
// hides edges. The FALSE path re-probes, rate-limited, so staleness is bounded
// by derivedRecheck rather than by process lifetime.
func (b *backend) derivedExists() bool {
	b.derivedMu.RLock()
	known, at := b.hasDerived, b.probedAt
	b.derivedMu.RUnlock()
	if known {
		return true
	}
	if !at.IsZero() && time.Since(at) < derivedRecheck {
		return false
	}

	b.derivedMu.Lock()
	defer b.derivedMu.Unlock()
	if b.hasDerived || (!b.probedAt.IsZero() && time.Since(b.probedAt) < derivedRecheck) {
		return b.hasDerived
	}
	var n bool
	err := b.pool.QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	b.hasDerived = err != nil || n
	b.probedAt = time.Now()
	return b.hasDerived
}

// derivedExistsFresh probes WITHOUT the rate-limited cache — write paths
// (InvalidateFunctional) need it: the cached guard's fail-safe is tuned for
// reads, and a stale false silently skips derived invalidation (see the
// SQLite twin). Errors (no such table) return false: skipping the UPDATE is
// safe, running it would fail the tx.
func (b *backend) derivedExistsFresh() bool {
	var n bool
	err := b.pool.QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	return err == nil && n
}

// markDerivedWritten is called after any insert into derived_relations.
func (b *backend) markDerivedWritten() {
	b.derivedMu.Lock()
	b.hasDerived = true
	b.probedAt = time.Now()
	b.derivedMu.Unlock()
}

// markDerivedMaybeEmpty re-probes after a delete — the only path back to false.
// The probe runs UNDER the lock, so a concurrent markDerivedWritten cannot be
// clobbered back to false.
func (b *backend) markDerivedMaybeEmpty() {
	b.derivedMu.Lock()
	defer b.derivedMu.Unlock()
	var n bool
	err := b.pool.QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	b.hasDerived = err != nil || n
	b.probedAt = time.Now()
}

// unionIfDerived appends the derived arm when rows exist. Because $N
// placeholders are positional and reusable, the caller's args are unchanged.
func (s *ontologyStore) unionIfDerived(base, derivedPred string) string {
	if !s.b.derivedExists() {
		return base
	}
	return base + derivedArm(derivedPred)
}

// endpointSource builds the FROM body for aggregate reads that project
// endpoints rather than whole rows.
func (s *ontologyStore) endpointSource(predicate, derivedPred string) string {
	base := `SELECT source_id, target_id FROM relations`
	if predicate != "" {
		base += ` WHERE ` + predicate
	}
	if !s.b.derivedExists() {
		return base
	}
	return base + `
UNION ALL
SELECT d.source_id, d.target_id FROM derived_relations d
 WHERE ` + derivedPred + derivedNotShadowed
}
