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

// --- LinkAlias: non-destructive edge union ---

func seedEntity(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.AddEntity(Entity{ID: id, Type: TypeConcept, Name: id}); err != nil {
		t.Fatalf("AddEntity %s: %v", id, err)
	}
}

func relCount(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.RelationCount()
	if err != nil {
		t.Fatalf("RelationCount: %v", err)
	}
	return n
}

// The core contract: the canonical gains the alias's edges AND the alias keeps
// every one of its own. Nothing is deleted.
func TestLinkAliasCopiesEdgesAndAliasKeepsItsOwn(t *testing.T) {
	s := aliasStore(t)
	for _, id := range []string{"edwin", "buzz", "apollo", "nasa"} {
		seedEntity(t, s, id)
	}
	// edwin --extends--> apollo   (alias on the source side)
	// nasa  --cites----> edwin    (alias on the target side)
	if err := s.AddRelation(Relation{ID: "r1", SourceID: "edwin", TargetID: "apollo",
		Relation: RelExtends, Evidence: "edwin extends apollo", Confidence: 0.7, SourceDoc: "raw/edwin.md"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRelation(Relation{ID: "r2", SourceID: "nasa", TargetID: "edwin",
		Relation: RelCites, Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}
	before := relCount(t, s)

	res, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied))
	if err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}
	if res.Copied != 2 {
		t.Errorf("Copied = %d, want 2", res.Copied)
	}
	if res.AliasMissing || res.CanonicalMissing {
		t.Errorf("unexpected missing flags: %+v", res)
	}

	// The alias keeps its own edges — this is what "non-destructive" means.
	if n := relCount(t, s); n != before+2 {
		t.Errorf("relation count = %d, want %d (2 copies ADDED, none moved)", n, before+2)
	}
	aliasOut, err := s.GetRelations("edwin", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliasOut) != 1 || aliasOut[0].TargetID != "apollo" {
		t.Errorf("alias lost its outbound edge: %+v", aliasOut)
	}

	// The canonical gained both, with the alias edge's provenance carried over.
	canonOut, err := s.GetRelations("buzz", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(canonOut) != 1 || canonOut[0].TargetID != "apollo" {
		t.Fatalf("canonical missing the copied outbound edge: %+v", canonOut)
	}
	if canonOut[0].Evidence != "edwin extends apollo" || canonOut[0].SourceDoc != "raw/edwin.md" {
		t.Errorf("copy lost provenance: %+v", canonOut[0])
	}
	canonIn, err := s.GetRelations("buzz", Inbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(canonIn) != 1 || canonIn[0].SourceID != "nasa" {
		t.Errorf("canonical missing the copied inbound edge: %+v", canonIn)
	}

	// The alias row was recorded in the same transaction.
	if act, _ := s.GetActiveAlias("edwin"); act == nil || act.CanonicalID != "buzz" {
		t.Errorf("alias row not recorded: %+v", act)
	}
}

// A copy must NEVER overwrite an edge the canonical asserted itself. The
// confidence-guarded upsert AddRelation uses is sound only because both sides
// assert the SAME edge; here the winning side asserts a DIFFERENT one, so a
// DO UPDATE would write the alias's evidence onto the canonical's row — and it
// could never recover, because the next compile's honest re-assertion at the
// lower confidence loses to that same guard.
func TestLinkAliasDoesNotOverwriteCanonicalEvidence(t *testing.T) {
	s := aliasStore(t)
	for _, id := range []string{"edwin", "buzz", "apollo"} {
		seedEntity(t, s, id)
	}
	if err := s.AddRelation(Relation{ID: "own", SourceID: "buzz", TargetID: "apollo",
		Relation: RelExtends, Evidence: "buzz extends apollo", Confidence: 0.6, SourceDoc: "raw/buzz.md"}); err != nil {
		t.Fatal(err)
	}
	// The alias asserts the same (target, relation) with HIGHER confidence.
	if err := s.AddRelation(Relation{ID: "alias-edge", SourceID: "edwin", TargetID: "apollo",
		Relation: RelExtends, Evidence: "edwin extends apollo", Confidence: 0.9, SourceDoc: "raw/edwin.md"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied))
	if err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}
	if res.Copied != 0 || res.Skipped != 1 {
		t.Errorf("got Copied=%d Skipped=%d, want 0/1", res.Copied, res.Skipped)
	}

	canon, err := s.GetRelations("buzz", Outbound, RelExtends)
	if err != nil || len(canon) != 1 {
		t.Fatalf("GetRelations: %+v %v", canon, err)
	}
	if canon[0].Evidence != "buzz extends apollo" {
		t.Errorf("canonical's OWN evidence was overwritten by the copy: %q", canon[0].Evidence)
	}
	if canon[0].Confidence != 0.6 {
		t.Errorf("canonical's own confidence changed to %v", canon[0].Confidence)
	}
	if canon[0].SourceDoc != "raw/buzz.md" {
		t.Errorf("canonical's own source_doc was overwritten: %q", canon[0].SourceDoc)
	}
}

// An edge between the alias and its own canonical cannot be copied — it would
// be a self-loop, which AddRelation rejects. Under non-destructive semantics it
// is RETAINED where it is, not deleted.
func TestLinkAliasSelfLoopNotCopiedButRetained(t *testing.T) {
	s := aliasStore(t)
	seedEntity(t, s, "edwin")
	seedEntity(t, s, "buzz")
	if err := s.AddRelation(Relation{ID: "e-c", SourceID: "edwin", TargetID: "buzz",
		Relation: RelContradicts, Confidence: 0.4}); err != nil {
		t.Fatal(err)
	}

	res, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied))
	if err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}
	if res.SelfLoops != 1 || res.Copied != 0 {
		t.Errorf("got SelfLoops=%d Copied=%d, want 1/0", res.SelfLoops, res.Copied)
	}
	if n := relCount(t, s); n != 1 {
		t.Errorf("relation count = %d, want the original edge retained (1)", n)
	}
}

func TestLinkAliasMissingEndpoints(t *testing.T) {
	s := aliasStore(t)
	seedEntity(t, s, "present")

	res, err := s.LinkAlias(mkAlias("ghost", "present", store.AliasApplied))
	if err != nil {
		t.Fatalf("missing alias must not error: %v", err)
	}
	if !res.AliasMissing {
		t.Error("AliasMissing not set for an absent alias entity")
	}
	if act, _ := s.GetActiveAlias("ghost"); act != nil {
		t.Errorf("alias row written for a missing alias: %+v", act)
	}

	res, err = s.LinkAlias(mkAlias("present", "ghost", store.AliasApplied))
	if err != nil {
		t.Fatalf("missing canonical must not error: %v", err)
	}
	if !res.CanonicalMissing {
		t.Error("CanonicalMissing not set for an absent canonical entity")
	}
	if act, _ := s.GetActiveAlias("present"); act != nil {
		t.Errorf("alias row written for a missing canonical: %+v", act)
	}

	if _, err := s.LinkAlias(mkAlias("a", "a", store.AliasApplied)); err == nil {
		t.Error("self-alias must be an error")
	}
}

// The sweep re-runs this on every compile. It must converge, not accumulate.
func TestLinkAliasIsIdempotent(t *testing.T) {
	s := aliasStore(t)
	for _, id := range []string{"edwin", "buzz", "apollo"} {
		seedEntity(t, s, id)
	}
	if err := s.AddRelation(Relation{ID: "r1", SourceID: "edwin", TargetID: "apollo",
		Relation: RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}

	first, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied))
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := relCount(t, s)

	second, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied))
	if err != nil {
		t.Fatal(err)
	}
	if first.Copied != 1 {
		t.Errorf("first run Copied = %d, want 1", first.Copied)
	}
	if second.Copied != 0 || second.Skipped != 1 {
		t.Errorf("second run Copied=%d Skipped=%d, want 0/1", second.Copied, second.Skipped)
	}
	if n := relCount(t, s); n != afterFirst {
		t.Errorf("relation count grew on re-link: %d -> %d", afterFirst, n)
	}
}

// A 32-bit id collides by birthday around 65k copied edges, and a colliding id
// with a different (source,target,relation) is a NON-TARGET unique violation
// that the ON CONFLICT clause does not absorb — the edge would be dropped.
func TestLinkAliasCopiedRelationIDWidth(t *testing.T) {
	s := aliasStore(t)
	for _, id := range []string{"edwin", "buzz", "apollo"} {
		seedEntity(t, s, id)
	}
	if err := s.AddRelation(Relation{ID: "r1", SourceID: "edwin", TargetID: "apollo",
		Relation: RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkAlias(mkAlias("edwin", "buzz", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}

	canon, err := s.GetRelations("buzz", Outbound, "")
	if err != nil || len(canon) != 1 {
		t.Fatalf("GetRelations: %+v %v", canon, err)
	}
	id := canon[0].ID
	const prefix = "alias:"
	if len(id) != len(prefix)+16 {
		t.Errorf("copied relation id = %q (%d chars), want %q + 16 hex chars (8 bytes)",
			id, len(id), prefix)
	}
}
