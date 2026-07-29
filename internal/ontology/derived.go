package ontology

import (
	"strings"
	"sync"
	"time"
)

// Alias-derived edges (decision-035).
//
// LinkAlias used to copy an alias's edges into `relations`, where a copy is
// indistinguishable from an original — which is why a link could never be
// undone. Copies now live in `derived_relations`, each stamped with the alias
// that caused it, and reads union the two tables.
//
// The union is written INTO each query rather than hidden behind a view,
// deliberately and on measurement: SQLite will not push a `source_id=? OR
// target_id=?` predicate through `UNION ALL`, so a view's derived arm plans as a
// full table scan on every point read. Repeating the predicate in both arms
// plans as MULTI-INDEX OR against idx_derived_source/target instead — measured
// 2.15x versus a view that degraded without bound in the derived population.

// derivedArm is the second half of the union. Three conditions, three jobs:
//
//   - the caller's own predicate, repeated so this arm can seek (see above);
//   - not shadowed by an original — this restores the precedence LinkAlias's
//     ON CONFLICT DO NOTHING gives today, so a derived row can never displace
//     an edge the canonical asserted itself. Keyed on source_id rather than
//     `r.id IS NULL`, because relations.id may legitimately be NULL;
//   - lowest alias_id wins, so two aliases deriving one edge return one row.
//     DISTINCT cannot do this: each row carries its own alias's evidence, so
//     the rows are genuinely distinct.
//
// Column names resolve to the derived table unqualified — the subqueries are
// scoped — so relationCols serves both arms unchanged.
func derivedArm(predicate string) string {
	return `
SELECT ` + relationCols + ` FROM derived_relations d
 WHERE ` + predicate + `
   AND NOT EXISTS (SELECT 1 FROM relations r
                    WHERE r.source_id=d.source_id AND r.target_id=d.target_id
                      AND r.relation=d.relation)
   AND NOT EXISTS (SELECT 1 FROM derived_relations d2
                    WHERE d2.source_id=d.source_id AND d2.target_id=d.target_id
                      AND d2.relation=d.relation AND d2.alias_id < d.alias_id)`
}

// unionIfDerived returns base unchanged when no derived rows exist, and base
// UNION ALL the derived arm when they do. Callers that bind placeholders must
// repeat their args when the arm is appended — check derivedExists() rather
// than inspecting the returned string.
//
// The guard is what keeps this free for anyone who never enables entity
// resolution: measured, the false branch is byte-identical to the pre-change
// statement and costs 1.00x. The true branch costs ~2.15x on point reads.
//
// derivedPred is base's predicate rewritten against `d`. Pass "1=1" for
// whole-table reads.
func (s *Store) unionIfDerived(base, derivedPred string) string {
	if !s.derivedExists() {
		return base
	}
	return base + "\nUNION ALL" + derivedArm(derivedPred)
}

// derivedRecheck bounds how stale a FALSE guard may be. A true guard is never
// re-probed: it can only become wrong by being slow, and markDerivedMaybeEmpty
// is the one path that clears it.
const derivedRecheck = time.Second

// derivedExists reports whether derived_relations has any row.
//
// It fails safe in one direction: a stale true is merely slower, a stale false
// HIDES EDGES. The original design probed once per store and treated that as
// sufficient — it was not. A store that has only ever seen an empty table has no
// way to learn that another process (or another store over the same database)
// wrote a derived row, so `serve` would omit every alias-derived edge from its
// graph view until restart. Before decision-035 those edges lived in `relations`
// and were visible immediately, so a once-only probe was a regression, not just
// a limitation.
//
// So the FALSE path re-probes, rate-limited. Staleness is bounded by
// derivedRecheck instead of by process lifetime, and the amortised cost is one
// 5.4us query per second per store rather than one per read.
func (s *Store) derivedExists() bool {
	s.derivedMu.RLock()
	known, at := s.hasDerived, s.probedAt
	s.derivedMu.RUnlock()
	if known {
		return true
	}
	if !at.IsZero() && time.Since(at) < derivedRecheck {
		return false
	}

	s.derivedMu.Lock()
	defer s.derivedMu.Unlock()
	// Re-check under the write lock: another goroutine may have probed or
	// marked while we waited.
	if s.hasDerived || (!s.probedAt.IsZero() && time.Since(s.probedAt) < derivedRecheck) {
		return s.hasDerived
	}
	var n int
	err := s.db.ReadDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	s.hasDerived = err != nil || n == 1
	s.probedAt = time.Now()
	return s.hasDerived
}

// derivedExistsFresh probes WITHOUT the rate-limited cache. Write paths
// (InvalidateFunctional) use this: the cached guard's fail-safe is tuned for
// reads ("stale false HIDES EDGES" is worse than slow), but on a write a
// stale false silently skips derived invalidation — under compile-vs-serve
// concurrency another process can land the first derived row inside the 1s
// cache window. One 5µs EXISTS query per supersession removes the window.
// Errors (e.g. a pre-v12 schema with no derived_relations) return false:
// skipping the UPDATE is safe, running it would fail the whole tx.
func (s *Store) derivedExistsFresh() bool {
	var n int
	err := s.db.ReadDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	return err == nil && n == 1
}

// markDerivedWritten is called after any insert into derived_relations. Setting
// the flag true is always safe.
func (s *Store) markDerivedWritten() {
	s.derivedMu.Lock()
	s.hasDerived = true
	s.probedAt = time.Now()
	s.derivedMu.Unlock()
}

// markDerivedMaybeEmpty re-probes after a delete — the only path that may set
// the flag back to false, and it re-reads rather than assuming.
//
// The probe runs UNDER the lock. Probing outside it and assigning after lets a
// concurrent markDerivedWritten be clobbered back to false, which is the one
// direction this design must never go.
func (s *Store) markDerivedMaybeEmpty() {
	s.derivedMu.Lock()
	defer s.derivedMu.Unlock()
	var n int
	err := s.db.ReadDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM derived_relations)`).Scan(&n)
	s.hasDerived = err != nil || n == 1
	s.probedAt = time.Now()
}

// derivedNotShadowed is the two anti-joins, for callers that project a single
// column rather than a whole relation row.
const derivedNotShadowed = `
   AND NOT EXISTS (SELECT 1 FROM relations r
                    WHERE r.source_id=d.source_id AND r.target_id=d.target_id
                      AND r.relation=d.relation)
   AND NOT EXISTS (SELECT 1 FROM derived_relations d2
                    WHERE d2.source_id=d.source_id AND d2.target_id=d.target_id
                      AND d2.relation=d.relation AND d2.alias_id < d.alias_id)`

// dupIf repeats the argument list when the derived arm was appended, because
// that arm binds the same placeholders again.
//
// It takes the guard's value rather than reading it: a caller that reads the
// guard for the SQL and again for the args can be caught by a concurrent write
// between the two, yielding a placeholder/argument mismatch at runtime.
func dupIf(derived bool, args ...any) []any {
	if !derived {
		return args
	}
	return append(append([]any{}, args...), args...)
}

// derivedGuard is embedded in Store.
type derivedGuard struct {
	derivedMu  sync.RWMutex
	hasDerived bool
	probedAt   time.Time // zero = never probed
}

// countingSource builds the FROM clause for aggregate reads (degree, connection
// counts) that select endpoints rather than full rows.
func (s *Store) endpointSource(predicate, derivedPred string) string {
	return s.endpointSourceWith(s.derivedExists(), predicate, derivedPred)
}

// endpointSourceWith takes the guard's value so a caller can read it once and
// use the same answer for both the SQL and its arguments.
func (s *Store) endpointSourceWith(derived bool, predicate, derivedPred string) string {
	base := `SELECT source_id, target_id FROM relations`
	if strings.TrimSpace(predicate) != "" {
		base += ` WHERE ` + predicate
	}
	if !derived {
		return base
	}
	arm := `
SELECT d.source_id, d.target_id FROM derived_relations d
 WHERE ` + derivedPred + `
   AND NOT EXISTS (SELECT 1 FROM relations r
                    WHERE r.source_id=d.source_id AND r.target_id=d.target_id
                      AND r.relation=d.relation)
   AND NOT EXISTS (SELECT 1 FROM derived_relations d2
                    WHERE d2.source_id=d.source_id AND d2.target_id=d.target_id
                      AND d2.relation=d.relation AND d2.alias_id < d.alias_id)`
	return base + "\nUNION ALL" + arm
}
