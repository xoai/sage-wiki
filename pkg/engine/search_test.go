package engine

import (
	"context"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/provider/providerfake"
)

// TestSearchFacade: FTS-seeded workspace → engine Search finds the doc;
// an injected pkg/provider supplies embeddings.
func TestSearchFacade(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir, WithProvider(providerfake.New("x")))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := w.app.Mem.Add(memory.Entry{
		ID: "concept:attention", Content: "Self-attention computes pairwise token affinities.",
		ArticlePath: "wiki/concepts/attention.md",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := w.Search(context.Background(), SearchRequest{Query: "pairwise token affinities", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if res.Results[0].DocID == "" {
		t.Error("result must carry a DocID")
	}

	if _, err := w.Search(context.Background(), SearchRequest{Query: "x", Channels: []string{"nope"}}); err == nil {
		t.Error("unknown channel must error")
	}
}

// TestGraphAPI: seed entities + a temporal relation; query entities,
// relations, neighbors, and the AsOf view.
func TestGraphAPI(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ont := w.app.Ont
	if err := ont.AddEntity(store.Entity{ID: "e-a", Type: "concept", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := ont.AddEntity(store.Entity{ID: "e-b", Type: "concept", Name: "Beta", CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := ont.AddRelation(store.Relation{ID: "r1", SourceID: "e-a", TargetID: "e-b", Relation: ontology.RelCites, CreatedAt: "2026-01-02T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	g := w.Graph()
	ents, err := g.Entities(context.Background(), GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Errorf("Entities = %d, want 2", len(ents))
	}

	rels, err := g.Relations(context.Background(), GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Relation != ontology.RelCites {
		t.Errorf("Relations = %+v", rels)
	}

	nb, err := g.Neighbors(context.Background(), "e-a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb) == 0 {
		t.Error("Neighbors of e-a must include e-b")
	}

	// AsOf view is a valid handle (temporal behavior store-dependent).
	if g.AsOf(time2026()) == nil {
		t.Error("AsOf must return a GraphAPI")
	}
}

func time2026() time.Time {
	return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
}

// TestGraphAPIAsOfSemantics pins Gate 8 M1: AsOf().Entities errors loudly;
// AsOf().Relations drops out-of-window edges.
func TestGraphAPIAsOfSemantics(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ont := w.app.Ont
	ont.AddEntity(store.Entity{ID: "e-a", Type: "concept", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"})
	ont.AddEntity(store.Entity{ID: "e-b", Type: "concept", Name: "Beta", CreatedAt: "2026-01-01T00:00:00Z"})
	ont.AddRelation(store.Relation{ID: "r1", SourceID: "e-a", TargetID: "e-b", Relation: ontology.RelCites, CreatedAt: "2026-01-02T00:00:00Z", ValidFrom: "2026-03-01T00:00:00Z"})

	past := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)  // before ValidFrom
	later := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // inside the window

	if _, err := w.Graph().AsOf(past).Entities(context.Background(), GraphFilter{}); err == nil {
		t.Error("AsOf().Entities must error loudly, not return current data")
	}

	rels, err := w.Graph().AsOf(past).Relations(context.Background(), GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 0 {
		t.Errorf("edge not yet valid at %v must be filtered, got %d", past, len(rels))
	}
	rels, err = w.Graph().AsOf(later).Relations(context.Background(), GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("edge valid at %v must be returned, got %d", later, len(rels))
	}
}
