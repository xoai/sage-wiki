package compiler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// T3.10 — the transitive case, and the reason the sweep had to become a rebuild.
//
// A→B→C: LinkAlias(B→C) derives C-side rows FROM A's rows and stamps them B.
// So `--unlink A` cannot reach them by alias_id, and an insert-only re-sweep
// finds the edge already present and skips. Measured during the spike: the
// stale row survived. Only rebuilding from the surviving applied links clears it.
func TestUnlinkClearsTransitivelyDerivedRows(t *testing.T) {
	ont := passStore(t)
	for _, id := range []string{"A", "B", "C", "X"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Only A has a real edge.
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "A", TargetID: "X", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(alias, canon string) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canon, EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
	}
	if _, err := ont.LinkAlias(mk("A", "B")); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(mk("B", "C")); err != nil {
		t.Fatal(err)
	}

	// C sees X only because A's edge propagated through B.
	cRels, err := ont.GetRelations("C", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cRels) != 1 || cRels[0].TargetID != "X" {
		t.Fatalf("C should see X through the chain, got %+v", cRels)
	}

	// Undo the FIRST link, then re-sweep as the CLI does.
	if err := ont.UnlinkAlias("A", "B"); err != nil {
		t.Fatal(err)
	}
	SweepAliases(context.Background(), ont)

	// A's edge no longer justifies anything on B or C.
	cRels, err = ont.GetRelations("C", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cRels) != 0 {
		t.Errorf("C still sees %d edges after unlinking A — a transitively derived row survived: %+v",
			len(cRels), cRels)
	}
	bRels, err := ont.GetRelations("B", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bRels) != 0 {
		t.Errorf("B still sees %d edges after unlinking A: %+v", len(bRels), bRels)
	}
	// A keeps its own edge — this links, it does not collapse.
	aRels, err := ont.GetRelations("A", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(aRels) != 1 {
		t.Errorf("A lost its own edge: %+v", aRels)
	}
}

// N1 — the chain must converge regardless of `ListAliases` order.
//
// ListAliases returns `ORDER BY alias, canonical_id`, which is NOT topological.
// A->B->C happens to be visited outward, so the A/B/C fixture above passes even
// when the sweep only makes one pass. Name the same chain backwards and a
// single-pass rebuild DESTROYS it: it clears, then replays yy->xx before
// zz->yy, so zz's edges never reach xx and nothing puts them back.
//
// Roughly half of two-hop chains have this shape. The sweep runs on every
// compile, so a single-pass rebuild truncated them permanently and repeatedly.
func TestSweepConvergesForReverseAlphabeticalChains(t *testing.T) {
	ont := passStore(t)
	for _, id := range []string{"zz", "yy", "xx", "edge"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Only zz — the deepest alias — owns a real edge.
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "zz", TargetID: "edge", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(alias, canon string) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canon, EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
	}
	// Applied in the correct order, as --apply would.
	if _, err := ont.LinkAlias(mk("zz", "yy")); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(mk("yy", "xx")); err != nil {
		t.Fatal(err)
	}
	before, err := ont.GetRelations("xx", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("setup: xx should see the edge through the chain, got %d", len(before))
	}

	// A no-op sweep must leave it intact.
	SweepAliases(context.Background(), ont)
	after, err := ont.GetRelations("xx", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("a no-op sweep destroyed a chained edge: xx saw %d before, %d after — "+
			"the replay is not converging for chains that run against alphabetical order",
			len(before), len(after))
	}
}

// failRejectionsStore makes ListAliases(AliasRejected) fail while leaving every
// other read intact — the transient-error shape the sweep must survive.
type failRejectionsStore struct {
	store.OntologyStore
	err error
}

func (f failRejectionsStore) ListAliases(s store.AliasStatus) ([]store.EntityAlias, error) {
	if s == store.AliasRejected {
		return nil, f.err
	}
	return f.OntologyStore.ListAliases(s)
}

// N2 — the sweep clears derived edges and the replay puts them back, so EVERY
// early return must come before the clear. M1 moved the cancellation check
// above it and stopped one statement short: a failed rejection read still
// cleared first and then bailed, wiping every derived edge with nothing to
// restore them.
func TestFailedRejectionReadDoesNotWipeDerivedEdges(t *testing.T) {
	ont := passStore(t)
	for _, id := range []string{"A", "C", "X"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "A", TargetID: "X", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "C", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ont.GetRelations("C", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("setup: C should see 1 derived edge, got %d", len(before))
	}

	SweepAliases(context.Background(), failRejectionsStore{
		OntologyStore: ont, err: errors.New("transient"),
	})

	after, err := ont.GetRelations("C", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a failed rejection read wiped derived edges: C saw %d before, %d after — "+
			"the clear must come after every early return", len(before), len(after))
	}
}

// NEW-1 — the fixpoint must not multiply the structural counters.
//
// Pass 0 sees the real link set; every later pass re-walks the same links and
// re-observes the same facts. Accumulating them reported "2 skipped as rejected"
// for ONE rejection, and "already present" against a canonical asserting
// nothing — that was pass 1 meeting pass 0's own derived row. These numbers are
// printed by `ontology resolve --sweep`.
func TestSweepCountersAreNotMultipliedByPasses(t *testing.T) {
	ont := passStore(t)
	for _, id := range []string{"aa", "bb", "cc", "edge", "gone"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "aa", TargetID: "edge", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(alias, canon string, st store.AliasStatus) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canon, EntityType: "concept",
			Status: st, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
	}
	// One real chain link, so at least two passes run.
	if _, err := ont.LinkAlias(mk("aa", "bb", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	// One applied link whose alias no longer exists — exactly one missing endpoint.
	if err := ont.PutAlias(mk("gone", "cc", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	if err := ont.DeleteEntity("gone"); err != nil {
		t.Fatal(err)
	}
	// One applied link whose pair the user rejected in the REVERSE direction —
	// the sweep must skip it, count it ONCE, and RejectedSkipped is the only
	// set that had no assertion anywhere until this line (deleting its insert
	// left the whole suite green — the fifth review found it).
	if err := ont.PutAlias(mk("bb", "cc", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(mk("cc", "bb", store.AliasRejected)); err != nil {
		t.Fatal(err)
	}

	res := SweepAliases(context.Background(), ont)

	if res.RejectedSkipped != 1 {
		t.Errorf("RejectedSkipped = %d, want 1 — one applied link sits across a rejected "+
			"pair; later passes re-observing it must not re-count it, and dropping the "+
			"count entirely must not go unnoticed", res.RejectedSkipped)
	}

	if res.EndpointMissing != 1 {
		t.Errorf("EndpointMissing = %d, want 1 — one link has a pruned endpoint, and "+
			"re-walking it on later passes must not re-count it", res.EndpointMissing)
	}
	if res.AlreadyPresent != 0 {
		t.Errorf("AlreadyPresent = %d, want 0 — no canonical asserts an edge itself, so a "+
			"non-zero count is a later pass meeting pass 0's own derived row", res.AlreadyPresent)
	}
}

// NEW-2 — exhausting the pass cap must be loud. The sweep rebuilds, so a chain
// deeper than the cap is truncated identically on every compile; silence makes
// that indistinguishable from a healthy sweep. maxSweepPasses is maxAliasChain+1
// because N links need N passes to propagate and one more to observe a fixpoint.
func TestSweepPassCapIsChainDepthPlusOne(t *testing.T) {
	if maxSweepPasses != 33 {
		t.Errorf("maxSweepPasses = %d, want 33 (ontology.maxAliasChain 32 + one "+
			"confirming pass); at 32 a 32-link chain exits on the bound without "+
			"ever confirming convergence", maxSweepPasses)
	}
}

// failOnRevisitStore fails LinkAlias for one alias, but only from its second
// call onward — the shape of a transient failure landing on pass >= 1 of the
// fixpoint (SQLITE_BUSY from a concurrent writer, a Postgres blip, or a
// concurrent --reject flipping the row between passes).
type failOnRevisitStore struct {
	store.OntologyStore
	alias string
	calls map[string]int
}

func (f *failOnRevisitStore) LinkAlias(a store.EntityAlias) (store.LinkResult, error) {
	f.calls[a.Alias]++
	if a.Alias == f.alias && f.calls[a.Alias] >= 2 {
		return store.LinkResult{}, errors.New("transient")
	}
	return f.OntologyStore.LinkAlias(a)
}

// Round-4 regression — a replay failure on pass >= 1 must surface in Failed.
//
// The counter scoping that stopped the fixpoint from multiplying structural
// counts took Failed from pass 0 alone, so a link that failed only on a later
// pass reported a CLEAN sweep — while the rebuild had cleared the derived edges
// that failed replay was supposed to restore. `--sweep` exits nonzero on
// Failed>0 precisely so scripts can detect breakage; this hid it.
func TestSweepReportsFailuresFromLaterPasses(t *testing.T) {
	base := passStore(t)
	for _, id := range []string{"ca", "cb", "cc", "edge"} {
		if err := base.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Only the deepest alias owns a real edge; anti-topological naming means
	// cb->ca is visited before cc->cb, so cb's re-visit on pass 1 is what
	// propagates cc's edge to ca — and that is the call we make fail.
	if err := base.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "cc", TargetID: "edge", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(alias, canon string) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canon, EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
	}
	if _, err := base.LinkAlias(mk("cc", "cb")); err != nil {
		t.Fatal(err)
	}
	if _, err := base.LinkAlias(mk("cb", "ca")); err != nil {
		t.Fatal(err)
	}

	ont := &failOnRevisitStore{OntologyStore: base, alias: "cb", calls: map[string]int{}}
	res := SweepAliases(context.Background(), ont)

	if res.Failed == 0 {
		t.Error("a replay failure on pass >= 1 reported Failed=0 — the sweep cleared " +
			"derived edges, failed to restore them, and claimed clean success; " +
			"--sweep would exit 0 on a stripped graph")
	}
}

// The pass-cap warn was shipped unpinned in round 4 — deleting it left every
// test green. This pins the behaviour, not the constant: a chain deeper than
// the cap must WARN (it is truncated, and re-truncated every compile), and an
// ordinary chain must not.
func TestSweepWarnsWhenPassCapExhausted(t *testing.T) {
	out := captureWarns(t)
	ont := passStore(t)

	// maxSweepPasses+1 links, named so ListAliases' alphabetical order is
	// anti-topological: each pass propagates exactly one hop, so the loop
	// exhausts the cap with edges still moving.
	depth := maxSweepPasses + 1
	for i := 0; i <= depth; i++ {
		if err := ont.AddEntity(ontology.Entity{
			ID: fmt.Sprintf("z%02d", i), Type: "concept", Name: "z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddEntity(ontology.Entity{ID: "payload", Type: "concept", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	// The deepest alias owns the only real edge.
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: fmt.Sprintf("z%02d", depth), TargetID: "payload",
		Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	// Applied rows only — LinkAlias would derive eagerly and shrink the chain.
	for i := depth; i >= 1; i-- {
		if err := ont.PutAlias(store.EntityAlias{
			Alias: fmt.Sprintf("z%02d", i), CanonicalID: fmt.Sprintf("z%02d", i-1),
			EntityType: "concept", Status: store.AliasApplied, Source: "llm",
			CreatedAt: "2026-07-27T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}

	SweepAliases(context.Background(), ont)

	if !strings.Contains(out(), "pass cap") {
		t.Errorf("a chain deeper than maxSweepPasses swept without the exhaustion warn — "+
			"truncation recurs on every compile and is invisible without it:\n%s", out())
	}
}

// ...and the warn must NOT fire for a chain the cap accommodates.
func TestSweepDoesNotWarnBelowThePassCap(t *testing.T) {
	out := captureWarns(t)
	ont := passStore(t)
	for _, id := range []string{"za", "zb", "zc", "payload"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "zc", TargetID: "payload", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{"zc", "zb"}, {"zb", "za"}} {
		if err := ont.PutAlias(store.EntityAlias{
			Alias: p[0], CanonicalID: p[1], EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}

	SweepAliases(context.Background(), ont)

	if strings.Contains(out(), "pass cap") {
		t.Errorf("a two-link chain triggered the exhaustion warn:\n%s", out())
	}
}
