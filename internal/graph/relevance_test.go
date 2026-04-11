package graph

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestStore(t *testing.T) *ontology.Store {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ontology.NewStore(db, ontology.ValidRelationNames(ontology.BuiltinRelations))
}

// buildTestGraph creates:
//
//	Concepts: attention, transformer, lstm, embedding
//	Sources:  raw/paper.pdf, raw/rnn-book.pdf, raw/nlp-guide.md
//	Relations:
//	  attention   --cites--> raw/paper.pdf
//	  transformer --cites--> raw/paper.pdf       (source overlap with attention)
//	  transformer --extends--> attention          (direct link)
//	  lstm        --cites--> raw/rnn-book.pdf
//	  embedding   --cites--> raw/paper.pdf        (source overlap with attention)
//	  embedding   --cites--> raw/nlp-guide.md
//	  embedding   --extends--> attention           (direct link)
func buildTestGraph(t *testing.T, store *ontology.Store) {
	t.Helper()

	entities := []ontology.Entity{
		{ID: "attention", Type: "concept", Name: "Attention", ArticlePath: "wiki/concepts/attention.md"},
		{ID: "transformer", Type: "technique", Name: "Transformer", ArticlePath: "wiki/concepts/transformer.md"},
		{ID: "lstm", Type: "technique", Name: "LSTM", ArticlePath: "wiki/concepts/lstm.md"},
		{ID: "embedding", Type: "concept", Name: "Embedding", ArticlePath: "wiki/concepts/embedding.md"},
		{ID: "raw/paper.pdf", Type: "source", Name: "paper.pdf"},
		{ID: "raw/rnn-book.pdf", Type: "source", Name: "rnn-book.pdf"},
		{ID: "raw/nlp-guide.md", Type: "source", Name: "nlp-guide.md"},
	}
	for _, e := range entities {
		if err := store.AddEntity(e); err != nil {
			t.Fatalf("AddEntity %s: %v", e.ID, err)
		}
	}

	relations := []ontology.Relation{
		{ID: "c1", SourceID: "attention", TargetID: "raw/paper.pdf", Relation: "cites"},
		{ID: "c2", SourceID: "transformer", TargetID: "raw/paper.pdf", Relation: "cites"},
		{ID: "c3", SourceID: "lstm", TargetID: "raw/rnn-book.pdf", Relation: "cites"},
		{ID: "c4", SourceID: "embedding", TargetID: "raw/paper.pdf", Relation: "cites"},
		{ID: "c5", SourceID: "embedding", TargetID: "raw/nlp-guide.md", Relation: "cites"},
		{ID: "r1", SourceID: "transformer", TargetID: "attention", Relation: "extends"},
		{ID: "r2", SourceID: "embedding", TargetID: "attention", Relation: "extends"},
	}
	for _, r := range relations {
		if err := store.AddRelation(r); err != nil {
			t.Fatalf("AddRelation %s: %v", r.ID, err)
		}
	}
}

func TestScoreRelevance_DirectLink(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	// Seed: attention. Transformer and embedding extend attention → direct link.
	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 10,
		MaxDepth:  2,
		Weights:   RelevanceWeights{DirectLink: 1.0, SourceOverlap: 0, CommonNeighbor: 0, TypeAffinity: 0},
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	found := make(map[string]bool)
	for _, r := range results {
		found[r.EntityID] = true
		if r.Signals["direct_link"] <= 0 {
			t.Errorf("%s: expected direct_link > 0, got %f", r.EntityID, r.Signals["direct_link"])
		}
	}

	if !found["transformer"] {
		t.Error("expected transformer (extends attention)")
	}
	if !found["embedding"] {
		t.Error("expected embedding (extends attention)")
	}
	if found["lstm"] {
		t.Error("lstm should not appear (no direct link to attention)")
	}
}

func TestScoreRelevance_SourceOverlap(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	// Seed: attention. Transformer and embedding also cite raw/paper.pdf.
	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 10,
		MaxDepth:  1,
		Weights:   RelevanceWeights{DirectLink: 0, SourceOverlap: 1.0, CommonNeighbor: 0, TypeAffinity: 0},
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	found := make(map[string]bool)
	for _, r := range results {
		found[r.EntityID] = true
	}

	if !found["transformer"] {
		t.Error("expected transformer (shares raw/paper.pdf)")
	}
	if !found["embedding"] {
		t.Error("expected embedding (shares raw/paper.pdf)")
	}
	if found["lstm"] {
		t.Error("lstm should not appear (different source)")
	}
}

func TestScoreRelevance_CombinedSignals(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 10,
		MaxDepth:  2,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results with default weights")
	}

	// Transformer and embedding should score highest (direct link + source overlap)
	for _, r := range results {
		if r.Score <= 0 {
			t.Errorf("%s: score should be > 0, got %f", r.EntityID, r.Score)
		}
	}

	// Source entities should be excluded
	for _, r := range results {
		if r.EntityID == "raw/paper.pdf" || r.EntityID == "raw/rnn-book.pdf" {
			t.Errorf("source entity %s should be excluded from results", r.EntityID)
		}
	}
}

func TestScoreRelevance_MaxExpandCap(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 1, // request only 1
		MaxDepth:  2,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	if len(results) > 1 {
		t.Errorf("expected at most 1 result, got %d", len(results))
	}
}

func TestScoreRelevance_EmptyGraph(t *testing.T) {
	store := setupTestStore(t)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"nonexistent"},
		MaxExpand: 10,
		MaxDepth:  2,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results from empty graph, got %d", len(results))
	}
}

func TestScoreRelevance_SelfExclusion(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 10,
		MaxDepth:  2,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	for _, r := range results {
		if r.EntityID == "attention" {
			t.Error("seed entity 'attention' should be excluded from results")
		}
	}
}

func TestScoreRelevance_SourceEntitiesExcluded(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   []string{"attention"},
		MaxExpand: 100,
		MaxDepth:  3,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}

	for _, r := range results {
		e, _ := store.GetEntity(r.EntityID)
		if e != nil && e.Type == "source" {
			t.Errorf("source entity %s should be excluded", r.EntityID)
		}
	}
}

func TestScoreRelevance_EmptySeeds(t *testing.T) {
	store := setupTestStore(t)
	buildTestGraph(t, store)

	results, err := ScoreRelevance(store, RelevanceOpts{
		SeedIDs:   nil,
		MaxExpand: 10,
		Weights:   DefaultWeights(),
	})
	if err != nil {
		t.Fatalf("ScoreRelevance: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty seeds, got %d results", len(results))
	}
}

func TestGetTypeAffinity_UnknownType(t *testing.T) {
	// Unknown types should get fallback 1.0
	val := getTypeAffinity("unknown", "concept")
	if val != 1.0 {
		t.Errorf("expected 1.0 fallback for unknown type, got %f", val)
	}

	val = getTypeAffinity("concept", "unknown")
	if val != 1.0 {
		t.Errorf("expected 1.0 fallback for unknown target type, got %f", val)
	}
}
