package compiler

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

func normStore(t *testing.T) *ontology.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	merged := ontology.MergedEntityTypes(nil)
	return ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(nil)),
		ontology.ValidEntityTypeNames(merged))
}

func names(g ExtractedGraph) map[string]string {
	m := map[string]string{}
	for _, e := range g.Entities {
		m[e.Name] = e.Type
	}
	return m
}

// A predicate the vocabulary does not know costs exactly ONE edge — the rest
// of the document's graph survives. That property is why TriplesSchema carries
// no enum: an enum would fail the whole call instead.
func TestNormalizeGraphUnknownPredicateDropsOneEdge(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "a", Type: "concept", Description: "A"},
			{Name: "b", Type: "concept", Description: "B"},
			{Name: "c", Type: "concept", Description: "C"},
		},
		Relations: []ExtractedRelation{
			{Source: "a", Predicate: "frobnicates", Target: "b", Evidence: "x", Confidence: 0.9},
			{Source: "a", Predicate: "extends", Target: "c", Evidence: "y", Confidence: 0.9},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 60)

	if len(got.Relations) != 1 {
		t.Fatalf("relations = %d, want 1 (only the unknown predicate dropped): %+v", len(got.Relations), got.Relations)
	}
	if got.Relations[0].Predicate != ontology.RelExtends {
		t.Errorf("surviving predicate = %q", got.Relations[0].Predicate)
	}
}

// A synonym maps onto its builtin relation type.
func TestNormalizeGraphMapsSynonymPredicate(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "a", Type: "concept", Description: "A"},
			{Name: "b", Type: "concept", Description: "B"},
		},
		Relations: []ExtractedRelation{
			{Source: "a", Predicate: "Builds On", Target: "b", Evidence: "x", Confidence: 0.5},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 60)
	if len(got.Relations) != 1 || got.Relations[0].Predicate != ontology.RelExtends {
		t.Fatalf("expected `builds on` to map to %q, got %+v", ontology.RelExtends, got.Relations)
	}
}

func TestNormalizeGraphEntityRules(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "  padded  ", Type: "concept", Description: "trimmed"},
			{Name: "", Type: "concept", Description: "dropped"},
			{Name: "oddly-typed", Type: "wormhole", Description: "coerced"},
			{Name: "partner", Type: "concept", Description: "P"},
		},
		Relations: []ExtractedRelation{
			{Source: "padded", Predicate: "extends", Target: "partner", Evidence: "e", Confidence: 0.7},
			{Source: "oddly-typed", Predicate: "extends", Target: "partner", Evidence: "e", Confidence: 0.7},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 60)
	byName := names(got)

	if _, ok := byName["padded"]; !ok {
		t.Error("name not trimmed")
	}
	if _, ok := byName[""]; ok {
		t.Error("empty-name entity survived")
	}
	if byName["oddly-typed"] != ontology.TypeConcept {
		t.Errorf("unknown type = %q, want coercion to %q", byName["oddly-typed"], ontology.TypeConcept)
	}
}

func TestNormalizeGraphRelationRules(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "a", Type: "concept", Description: "A"},
			{Name: "b", Type: "concept", Description: "B"},
		},
		Relations: []ExtractedRelation{
			{Source: "a", Predicate: "extends", Target: "a", Evidence: "self", Confidence: 0.9},
			{Source: "a", Predicate: "extends", Target: "ghost", Evidence: "dangling", Confidence: 0.9},
			{Source: "a", Predicate: "extends", Target: "b", Evidence: "ok", Confidence: 7.5},
			{Source: "b", Predicate: "cites", Target: "a", Evidence: "ok", Confidence: 0},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 60)

	if len(got.Relations) != 2 {
		t.Fatalf("relations = %d, want 2 (self-loop and dangling endpoint dropped): %+v",
			len(got.Relations), got.Relations)
	}
	for _, r := range got.Relations {
		if r.Source == r.Target {
			t.Error("self-loop survived")
		}
		if r.Confidence > 1 {
			t.Errorf("confidence not clamped: %v", r.Confidence)
		}
		// An LLM edge must be able to win the confidence-guarded upsert against
		// a keyword edge, which asserts 0 — so 0 is floored, not left at 0.
		if r.Confidence <= 0 {
			t.Errorf("confidence not floored above zero: %v", r.Confidence)
		}
	}
}

// Entity truncation runs BEFORE endpoint validation. Otherwise a surviving
// relation can point at a truncated entity and fail its foreign key at
// AddRelation, which the caller only sees as a log line.
func TestNormalizeGraphTruncatesEntitiesBeforeValidatingEndpoints(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "keep-a", Type: "concept", Description: "A"},
			{Name: "keep-b", Type: "concept", Description: "B"},
			{Name: "cut-me", Type: "concept", Description: "C"},
		},
		Relations: []ExtractedRelation{
			{Source: "keep-a", Predicate: "extends", Target: "keep-b", Evidence: "e", Confidence: 0.8},
			{Source: "keep-a", Predicate: "cites", Target: "cut-me", Evidence: "e", Confidence: 0.8},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 2, 60)

	if len(got.Entities) != 2 {
		t.Fatalf("entities = %d, want 2 after truncation", len(got.Entities))
	}
	for _, r := range got.Relations {
		if r.Target == "cut-me" || r.Source == "cut-me" {
			t.Error("a relation survived pointing at a truncated entity — it would fail its FK")
		}
	}
}

// An entity that ends up with no relations is dropped: keeping it would file a
// `orphan entity — no relations` lint finding with an empty path, in volume
// proportional to the model's predicate error rate.
func TestNormalizeGraphDropsEntitiesLeftWithoutRelations(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "connected-a", Type: "concept", Description: "A"},
			{Name: "connected-b", Type: "concept", Description: "B"},
			{Name: "lonely", Type: "concept", Description: "no edges survive"},
		},
		Relations: []ExtractedRelation{
			{Source: "connected-a", Predicate: "extends", Target: "connected-b", Evidence: "e", Confidence: 0.8},
			{Source: "lonely", Predicate: "frobnicates", Target: "connected-a", Evidence: "e", Confidence: 0.8},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 60)

	if _, ok := names(got)["lonely"]; ok {
		t.Error("entity with zero surviving relations was kept")
	}
	if len(got.Entities) != 2 {
		t.Errorf("entities = %d, want 2", len(got.Entities))
	}
}

func TestNormalizeGraphTruncatesRelations(t *testing.T) {
	defs := ontology.MergedRelations(nil)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "a", Type: "concept", Description: "A"},
			{Name: "b", Type: "concept", Description: "B"},
			{Name: "c", Type: "concept", Description: "C"},
		},
		Relations: []ExtractedRelation{
			{Source: "a", Predicate: "extends", Target: "b", Evidence: "e", Confidence: 0.8},
			{Source: "a", Predicate: "cites", Target: "c", Evidence: "e", Confidence: 0.8},
			{Source: "b", Predicate: "cites", Target: "c", Evidence: "e", Confidence: 0.8},
		},
	}
	got := normalizeGraph(g, normStore(t), defs, 40, 1)
	if len(got.Relations) != 1 {
		t.Fatalf("relations = %d, want 1 after truncation", len(got.Relations))
	}
}
