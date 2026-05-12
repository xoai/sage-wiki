package compiler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	return ontology.NewStore(db, nil, nil)
}

func TestExtractRelations_SameBlockCreatesRelation(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention implements [[self-attention]] for optimization."
	extractRelations("flash-attention", content, store, patterns)

	relations, err := store.ListRelations("", 100)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(relations) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(relations))
	}
	r := relations[0]
	if r.SourceID != "flash-attention" || r.TargetID != "self-attention" || r.Relation != "implements" {
		t.Errorf("unexpected relation: %s -[%s]-> %s", r.SourceID, r.Relation, r.TargetID)
	}
}

func TestExtractRelations_DifferentBlockNoRelation(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention is useful.\n\nIt implements optimization.\n\nSee [[self-attention]] for details."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 0 {
		t.Errorf("expected 0 relations (cross-block), got %d", len(relations))
	}
}

func TestExtractRelations_SingleParagraph(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention implements [[self-attention]] efficiently."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 relation for single paragraph, got %d", len(relations))
	}
}

func TestExtractRelations_SelfLinkSkipped(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention [[flash-attention]] implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Fatalf("expected 1 relation (self-link skipped), got %d", len(relations))
	}
	if relations[0].TargetID != "self-attention" {
		t.Errorf("TargetID = %q, want self-attention", relations[0].TargetID)
	}
}

func TestExtractRelations_MultipleWikilinksSameTarget(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "[[self-attention]] and also [[self-attention]] implements optimization."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 relation (deduplicated), got %d", len(relations))
	}
}

func TestExtractRelations_ValidSourcesFilters(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	content := "Flash attention implements [[self-attention]]."

	t.Run("excluded source type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidSources: []string{"concept"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 0 {
			t.Errorf("expected 0 (technique not in ValidSources [concept]), got %d", len(relations))
		}
	})

	t.Run("included source type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidSources: []string{"technique", "concept"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 1 {
			t.Errorf("expected 1 (technique in ValidSources), got %d", len(relations))
		}
	})
}

func TestExtractRelations_ValidTargetsFilters(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	content := "Flash attention implements [[self-attention]]."

	t.Run("excluded target type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"technique"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 0 {
			t.Errorf("expected 0 (concept not in ValidTargets), got %d", len(relations))
		}
	})

	t.Run("included target type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"concept", "technique"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 1 {
			t.Errorf("expected 1 (concept in ValidTargets), got %d", len(relations))
		}
	})
}

func TestExtractRelations_EmptyValidFiltersAllowsAll(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements", ValidSources: nil, ValidTargets: nil},
	}

	content := "Flash attention implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 (nil filters allow all), got %d", len(relations))
	}
}

func TestExtractRelations_EntityNotFoundWithValidTargets(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"concept"}},
	}

	content := "Flash attention implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 0 {
		t.Errorf("expected 0 (unknown target type '' not in ValidTargets), got %d", len(relations))
	}
}

func TestBuildFrontmatter_EmitsEntityType(t *testing.T) {
	concept := ExtractedConcept{
		Name:    "flash-attention",
		Aliases: []string{"fa"},
		Sources: []string{"raw/transformer.md"},
	}
	fields := map[string]string{"confidence": "high"}

	got := buildFrontmatter(concept, "technique", fields, nil, time.UTC)

	if !strings.Contains(got, "concept: flash-attention") {
		t.Error("missing concept field")
	}
	if !strings.Contains(got, "entity_type: technique") {
		t.Errorf("missing entity_type field; got:\n%s", got)
	}
	if !strings.Contains(got, "aliases:") {
		t.Error("missing aliases field")
	}
	if !strings.Contains(got, "sources:") {
		t.Error("missing sources field")
	}
	if !strings.Contains(got, "confidence: high") {
		t.Error("missing confidence field")
	}

	// Verify field order: concept → entity_type → aliases → sources → confidence
	conceptIdx := strings.Index(got, "concept:")
	entityIdx := strings.Index(got, "entity_type:")
	aliasIdx := strings.Index(got, "aliases:")
	srcIdx := strings.Index(got, "sources:")
	confIdx := strings.Index(got, "confidence:")
	if !(conceptIdx < entityIdx && entityIdx < aliasIdx && aliasIdx < srcIdx && srcIdx < confIdx) {
		t.Errorf("frontmatter field order incorrect:\n%s", got)
	}
}

func TestBuildFrontmatter_FallbackEntityType(t *testing.T) {
	concept := ExtractedConcept{
		Name:    "some-concept",
		Aliases: nil,
		Sources: nil,
	}
	fields := map[string]string{}

	// Caller passes "concept" as the resolved fallback for empty/invalid types
	got := buildFrontmatter(concept, "concept", fields, nil, time.UTC)

	if !strings.Contains(got, "entity_type: concept") {
		t.Errorf("expected entity_type: concept (fallback); got:\n%s", got)
	}
}
