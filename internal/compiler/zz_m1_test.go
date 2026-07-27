package compiler

import (
	"context"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// M1 — a cancelled compile must not destroy derived edges. The sweep clears
// before replaying, and this pass is deferred, so Ctrl-C reaches it: clearing
// before checking cancellation wiped every derived edge and returned.
func TestCancelledSweepDoesNotWipeDerivedEdges(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	SweepAliases(ctx, ont)

	after, err := ont.GetRelations("C", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a cancelled sweep destroyed derived edges: C saw %d before, %d after",
			len(before), len(after))
	}
}
