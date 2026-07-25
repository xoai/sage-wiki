package ontology

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

func aliasStore(t *testing.T) *Store { return setupTestDB(t) }

func mkAlias(alias, canonical string, status store.AliasStatus) store.EntityAlias {
	return store.EntityAlias{
		Alias:       alias,
		CanonicalID: canonical,
		EntityType:  TypeConcept,
		Status:      status,
		Confidence:  0.9,
		Reason:      "same referent",
		Source:      "llm",
		CreatedAt:   "2026-07-26T00:00:00Z",
		DecidedAt:   "2026-07-26T00:00:00Z",
		DecidedBy:   "auto",
	}
}

func TestPutAndGetActiveAlias(t *testing.T) {
	s := aliasStore(t)

	if err := s.PutAlias(mkAlias("edwin", "buzz", store.AliasApplied)); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}

	got, err := s.GetActiveAlias("edwin")
	if err != nil {
		t.Fatalf("GetActiveAlias: %v", err)
	}
	if got == nil {
		t.Fatal("GetActiveAlias returned nil for an applied row")
	}
	if got.CanonicalID != "buzz" || got.Status != store.AliasApplied {
		t.Errorf("got %+v, want canonical=buzz status=applied", got)
	}
	if got.Confidence != 0.9 || got.Reason != "same referent" || got.Source != "llm" {
		t.Errorf("audit fields not round-tripped: %+v", got)
	}
	if got.CreatedAt != "2026-07-26T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want the byte-identical RFC3339 string", got.CreatedAt)
	}

	// Absent alias is (nil, nil) — the legacy contract GetEntity uses.
	missing, err := s.GetActiveAlias("nobody")
	if err != nil || missing != nil {
		t.Errorf("GetActiveAlias(absent) = %v, %v; want nil, nil", missing, err)
	}
}

// A rejected row is not active: it must not surface from GetActiveAlias, or the
// pass would treat a rejected pair as a live link.
func TestGetActiveAliasIgnoresRejected(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("a", "b", store.AliasRejected)); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}
	got, err := s.GetActiveAlias("a")
	if err != nil {
		t.Fatalf("GetActiveAlias: %v", err)
	}
	if got != nil {
		t.Errorf("rejected row surfaced as active: %+v", got)
	}
}

// Rejection is a judgement that two entities are DIFFERENT. That does not depend
// on which one the model happened to nominate as canonical, so the check must
// match both orderings — otherwise re-rolling the direction bypasses the user.
func TestIsRejectedIsSymmetric(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("musician", "astronaut", store.AliasRejected)); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}

	forward, err := s.IsRejected("musician", "astronaut")
	if err != nil {
		t.Fatalf("IsRejected forward: %v", err)
	}
	if !forward {
		t.Error("IsRejected(musician, astronaut) = false, want true")
	}

	reverse, err := s.IsRejected("astronaut", "musician")
	if err != nil {
		t.Fatalf("IsRejected reverse: %v", err)
	}
	if !reverse {
		t.Error("IsRejected(astronaut, musician) = false — a swapped canonical bypassed the rejection")
	}

	unrelated, err := s.IsRejected("musician", "trumpeter")
	if err != nil {
		t.Fatalf("IsRejected unrelated: %v", err)
	}
	if unrelated {
		t.Error("IsRejected reported an unrelated pair as rejected")
	}
}

// The guarded upsert: an auto-applied re-proposal must never flip a human's
// rejection to applied, which would destroy the record of the decision.
func TestPutAliasCannotOverwriteRejected(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("a", "b", store.AliasRejected)); err != nil {
		t.Fatalf("seed rejected: %v", err)
	}

	applied := mkAlias("a", "b", store.AliasApplied)
	applied.Reason = "model says same"
	if err := s.PutAlias(applied); err != nil {
		t.Fatalf("PutAlias over rejected should be a silent no-op, got: %v", err)
	}

	rows, err := s.ListAliases(store.AliasRejected)
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rejected rows = %d, want 1", len(rows))
	}
	if rows[0].Reason != "same referent" {
		t.Errorf("rejected row was mutated: reason = %q", rows[0].Reason)
	}
	if act, _ := s.GetActiveAlias("a"); act != nil {
		t.Errorf("rejected row became active: %+v", act)
	}
}

// The sweep re-runs PutAlias against every applied row on every compile. If
// created_at or source were in the SET list, the audit trail's origin would be
// rewritten each run and could no longer explain the link.
func TestPutAliasPreservesOriginFields(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("a", "b", store.AliasApplied)); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}

	resweep := mkAlias("a", "b", store.AliasApplied)
	resweep.CreatedAt = "2027-01-01T00:00:00Z" // a later run's clock
	resweep.Source = "manual"
	resweep.EntityType = "technique"
	resweep.Confidence = 0.95
	if err := s.PutAlias(resweep); err != nil {
		t.Fatalf("PutAlias resweep: %v", err)
	}

	got, err := s.GetActiveAlias("a")
	if err != nil || got == nil {
		t.Fatalf("GetActiveAlias: %v %v", got, err)
	}
	if got.CreatedAt != "2026-07-26T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want the original preserved across a sweep", got.CreatedAt)
	}
	if got.Source != "llm" {
		t.Errorf("Source = %q, want the original preserved", got.Source)
	}
	if got.EntityType != TypeConcept {
		t.Errorf("EntityType = %q, want the original preserved", got.EntityType)
	}
	// Mutable decision fields DO update.
	if got.Confidence != 0.95 {
		t.Errorf("Confidence = %v, want 0.95 (decision fields are updatable)", got.Confidence)
	}
}

func TestListAliasesByStatus(t *testing.T) {
	s := aliasStore(t)
	for _, a := range []store.EntityAlias{
		mkAlias("a1", "c", store.AliasApplied),
		mkAlias("a2", "c", store.AliasApplied),
		mkAlias("p1", "c", store.AliasPending),
		mkAlias("r1", "c", store.AliasRejected),
	} {
		if err := s.PutAlias(a); err != nil {
			t.Fatalf("PutAlias %s: %v", a.Alias, err)
		}
	}

	applied, err := s.ListAliases(store.AliasApplied)
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(applied) != 2 {
		t.Errorf("applied = %d, want 2", len(applied))
	}
	// Deterministic order: the sweep replays these, and a nondeterministic
	// order makes the outcome depend on table layout.
	if len(applied) == 2 && applied[0].Alias > applied[1].Alias {
		t.Errorf("ListAliases not sorted: %q before %q", applied[0].Alias, applied[1].Alias)
	}

	pending, _ := s.ListAliases(store.AliasPending)
	if len(pending) != 1 || pending[0].Alias != "p1" {
		t.Errorf("pending = %+v, want [p1]", pending)
	}
}

func TestSetAliasStatus(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("a", "b", store.AliasPending)); err != nil {
		t.Fatalf("PutAlias: %v", err)
	}

	if err := s.SetAliasStatus("a", "b", store.AliasRejected, "user"); err != nil {
		t.Fatalf("SetAliasStatus: %v", err)
	}

	rejected, err := s.ListAliases(store.AliasRejected)
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(rejected) != 1 || rejected[0].DecidedBy != "user" {
		t.Fatalf("rejected = %+v, want one row decided_by=user", rejected)
	}
	if rejected[0].DecidedAt == "" {
		t.Error("DecidedAt not stamped on a status change")
	}
	if act, _ := s.GetActiveAlias("a"); act != nil {
		t.Errorf("row still active after rejection: %+v", act)
	}
}

func TestCanonicalIDFollowsAppliedChain(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("a", "b", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAlias(mkAlias("b", "c", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}

	got, err := s.CanonicalID("a")
	if err != nil {
		t.Fatalf("CanonicalID: %v", err)
	}
	if got != "c" {
		t.Errorf("CanonicalID(a) = %q, want c (chain a->b->c)", got)
	}

	// An id with no alias row resolves to itself.
	self, err := s.CanonicalID("z")
	if err != nil {
		t.Fatalf("CanonicalID(z): %v", err)
	}
	if self != "z" {
		t.Errorf("CanonicalID(z) = %q, want z", self)
	}
}

// A pending row is an UN-APPROVED proposal. Following it would merge into an
// entity a human never sanctioned, and one the alias was never compared against.
func TestCanonicalIDDoesNotFollowPending(t *testing.T) {
	s := aliasStore(t)
	if err := s.PutAlias(mkAlias("x", "y", store.AliasPending)); err != nil {
		t.Fatal(err)
	}

	got, err := s.CanonicalID("x")
	if err != nil {
		t.Fatalf("CanonicalID: %v", err)
	}
	if got != "x" {
		t.Errorf("CanonicalID(x) = %q, want x — a pending row must not be followed", got)
	}
}

func TestCanonicalIDBreaksCycle(t *testing.T) {
	s := aliasStore(t)
	// a->b->a. Reachable only through manual edits or a bug, but an infinite
	// walk in a compile pass is not an acceptable failure mode.
	if err := s.PutAlias(mkAlias("a", "b", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAlias(mkAlias("b", "a", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}

	done := make(chan string, 1)
	go func() {
		got, _ := s.CanonicalID("a")
		done <- got
	}()

	select {
	case got := <-done:
		if got != "a" {
			t.Errorf("CanonicalID(a) = %q, want the input id returned on a cycle", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CanonicalID looped forever on a cycle")
	}
}
