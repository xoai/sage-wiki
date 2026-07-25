package compiler

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

// Once the triple pass populates TYPED entities in Pass 2, keyword extraction
// in Pass 3 starts creating edges it previously skipped: extractRelations gates
// each pattern on the STORED entity types, and for an absent entity GetEntity
// returns (nil, nil) — so targetKnown is true with an empty type, which fails
// any ValidTargets list. This is a user-visible output change in enabled mode,
// not a code change, and it needs to be pinned.
func TestKeywordExtractionSeesTriplePassEntityTypes(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ont := ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.MergedRelations(nil)),
		ontology.ValidEntityTypeNames(ontology.MergedEntityTypes(nil)))

	patterns := []ontology.RelationPattern{{
		Keywords:     []string{"builds on"},
		Relation:     ontology.RelExtends,
		ValidTargets: []string{ontology.TypeConcept},
	}}

	// Source entity exists; target does NOT — the pre-P3-2 state.
	if err := ont.AddEntity(ontology.Entity{ID: "alpha", Type: ontology.TypeTechnique, Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	article := "Alpha builds on [[beta]] in every case."
	extractRelations("alpha", article, ont, patterns)

	if rels, _ := ont.GetRelations("alpha", ontology.Outbound, ""); len(rels) != 0 {
		t.Fatalf("expected no edge while the target entity is absent, got %+v", rels)
	}

	// Now the triple pass has created the target with a real type.
	if err := ont.AddEntity(ontology.Entity{ID: "beta", Type: ontology.TypeConcept, Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	extractRelations("alpha", article, ont, patterns)

	rels, _ := ont.GetRelations("alpha", ontology.Outbound, "")
	if len(rels) != 1 {
		t.Fatalf("keyword edge should now be created: %+v", rels)
	}
	// It carries no evidence — keyword edges never do; that is what makes the
	// confidence guard matter when both passes assert the same edge.
	if rels[0].Evidence != "" || rels[0].Confidence != 0 {
		t.Errorf("keyword edge unexpectedly evidenced: %+v", rels[0])
	}
}
