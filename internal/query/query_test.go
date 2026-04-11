package query

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/graph"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/storage"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"What is self-attention?", "what-is-self-attention"},
		{"How does Flash Attention work", "how-does-flash-attention-work"},
		{"", ""},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.expected {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSlugifyLong(t *testing.T) {
	long := "this is a very long question that should be truncated to fifty characters maximum for the filename"
	slug := slugify(long)
	if len(slug) > 50 {
		t.Errorf("slug too long: %d chars", len(slug))
	}
}

func TestExtractSeedIDsFromDocLevel(t *testing.T) {
	results := []hybrid.SearchResult{
		{ID: "concept:attention", ArticlePath: "wiki/concepts/attention.md"},
		{ID: "concept:transformer", ArticlePath: "wiki/concepts/transformer.md"},
		{ID: "concept:attention", ArticlePath: "wiki/concepts/attention.md"}, // dupe
		{ID: "", ArticlePath: ""}, // empty
	}

	ids := extractSeedIDsFromDocLevel(results)
	if len(ids) != 2 {
		t.Fatalf("expected 2 unique IDs, got %d: %v", len(ids), ids)
	}
	if ids[0] != "attention" || ids[1] != "transformer" {
		t.Errorf("expected [attention, transformer], got %v", ids)
	}
}

func TestExtractSeedIDsFromEnhanced(t *testing.T) {
	results := []search.SearchResult{
		{DocID: "concept:attention"},
		{DocID: "summary:paper"}, // should be skipped
		{DocID: "concept:transformer"},
	}

	ids := extractSeedIDsFromEnhanced(results)
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d: %v", len(ids), ids)
	}
}

func TestComputeGraphExpansion_EmptySeeds(t *testing.T) {
	cfg := &config.Config{Search: config.SearchConfig{}}
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ont := ontology.NewStore(db, nil)

	expanded := computeGraphExpansion(cfg, ont, nil)
	if expanded != nil {
		t.Errorf("expected nil for empty seeds, got %d", len(expanded))
	}
}

func TestComputeGraphExpansion_WithGraph(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ont := ontology.NewStore(db, nil)

	// Build graph: attention --extends--> transformer, both cite same source
	ont.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention", ArticlePath: "wiki/concepts/attention.md"})
	ont.AddEntity(ontology.Entity{ID: "transformer", Type: "technique", Name: "Transformer", ArticlePath: "wiki/concepts/transformer.md"})
	ont.AddEntity(ontology.Entity{ID: "raw/paper.pdf", Type: "source", Name: "paper"})
	ont.AddRelation(ontology.Relation{ID: "r1", SourceID: "transformer", TargetID: "attention", Relation: "extends"})
	ont.AddRelation(ontology.Relation{ID: "c1", SourceID: "attention", TargetID: "raw/paper.pdf", Relation: "cites"})
	ont.AddRelation(ontology.Relation{ID: "c2", SourceID: "transformer", TargetID: "raw/paper.pdf", Relation: "cites"})

	cfg := &config.Config{Search: config.SearchConfig{}}

	expanded := computeGraphExpansion(cfg, ont, []string{"attention"})
	if len(expanded) == 0 {
		t.Fatal("expected graph-expanded articles")
	}

	// transformer should be in results (direct link + source overlap)
	found := false
	for _, e := range expanded {
		if e.EntityID == "transformer" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected transformer in expanded results")
	}

	// Source entities should NOT be in results
	for _, e := range expanded {
		if e.EntityID == "raw/paper.pdf" {
			t.Error("source entity should be excluded")
		}
	}
}

func TestComputeGraphExpansion_DisabledByConfig(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ont := ontology.NewStore(db, nil)

	ont.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention", ArticlePath: "wiki/concepts/attention.md"})
	ont.AddEntity(ontology.Entity{ID: "transformer", Type: "concept", Name: "Transformer", ArticlePath: "wiki/concepts/transformer.md"})
	ont.AddRelation(ontology.Relation{ID: "r1", SourceID: "transformer", TargetID: "attention", Relation: "extends"})

	disabled := false
	cfg := &config.Config{Search: config.SearchConfig{GraphExpansion: &disabled}}

	// GraphExpansionEnabled() returns false → computeGraphExpansion should never be called
	// but if called anyway, it still returns results — the config check is at the caller
	if cfg.Search.GraphExpansionEnabled() {
		t.Error("expected graph expansion disabled")
	}
}

func TestGetTypeAffinity_ViaGraph(t *testing.T) {
	// Verify type affinity is used through the graph package
	val := graph.DefaultWeights()
	if val.TypeAffinity != 1.0 {
		t.Errorf("expected default type affinity weight 1.0, got %f", val.TypeAffinity)
	}
}
