package compiler

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

func persistStore(t *testing.T) *ontology.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.MergedRelations(nil)),
		ontology.ValidEntityTypeNames(ontology.MergedEntityTypes(nil)))
}

func twoNodeGraph() ExtractedGraph {
	return ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "backpressure", Type: ontology.TypeTechnique, Description: "Slows producers."},
			{Name: "flow-control", Type: ontology.TypeConcept, Description: "Regulates transfer rate."},
		},
		Relations: []ExtractedRelation{
			{Source: "backpressure", Predicate: ontology.RelExtends, Target: "flow-control",
				Evidence: "Backpressure extends flow control.", Confidence: 0.85},
		},
	}
}

func TestPersistGraphWritesEntitiesAndEvidencedRelations(t *testing.T) {
	ont := persistStore(t)
	if e, r, _, _ := persistGraph(ont, twoNodeGraph(), nil, "raw/paper.pdf", "2026-01-01T00:00:00Z", temporalHooks{}); e != 2 || r != 1 {
		t.Fatalf("persistGraph wrote %d entities / %d relations, want 2/1", e, r)
	}

	e, err := ont.GetEntity("backpressure")
	if err != nil || e == nil {
		t.Fatalf("GetEntity: %v %v", e, err)
	}
	if e.Definition != "Slows producers." {
		t.Errorf("description not persisted: %q — it is P3-3's disambiguation input", e.Definition)
	}
	if e.Name != "Backpressure" {
		t.Errorf("Name = %q, want the formatted display name", e.Name)
	}
	if e.ArticlePath != "" {
		t.Errorf("ArticlePath = %q, want empty — Pass 3 owns it", e.ArticlePath)
	}

	rels, err := ont.GetRelations("backpressure", ontology.Outbound, "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("GetRelations: %+v %v", rels, err)
	}
	r := rels[0]
	if r.Evidence != "Backpressure extends flow control." || r.Confidence != 0.85 {
		t.Errorf("evidence/confidence not persisted: %+v", r)
	}
	if r.SourceDoc != "raw/paper.pdf" {
		t.Errorf("SourceDoc = %q", r.SourceDoc)
	}
}

// Two distinct triples must not collide on the relation id.
//
// Under write.go's `source + "-" + predicate + "-" + target` scheme these two
// both render "a-extends-b-extends-c":
//
//	("a",           extends, "b-extends-c")
//	("a-extends-b", extends, "c")
//
// Entity names are lowercase-hyphenated slugs, so a name containing a predicate
// word is ordinary. The collision lands on the PRIMARY KEY — a conflict
// AddRelation's ON CONFLICT(source_id, target_id, relation) target does not
// cover — so the insert errors and the second edge is lost to a log line.
func TestPersistGraphRelationIDsAreCollisionFree(t *testing.T) {
	ont := persistStore(t)
	g := ExtractedGraph{
		Entities: []ExtractedEntity{
			{Name: "a", Type: ontology.TypeConcept, Description: "x"},
			{Name: "b-extends-c", Type: ontology.TypeConcept, Description: "y"},
			{Name: "a-extends-b", Type: ontology.TypeConcept, Description: "z"},
			{Name: "c", Type: ontology.TypeConcept, Description: "w"},
		},
		Relations: []ExtractedRelation{
			{Source: "a", Predicate: ontology.RelExtends, Target: "b-extends-c", Evidence: "e1", Confidence: 0.8},
			{Source: "a-extends-b", Predicate: ontology.RelExtends, Target: "c", Evidence: "e2", Confidence: 0.8},
		},
	}
	if _, r, _, _ := persistGraph(ont, g, nil, "raw/a.md", "", temporalHooks{}); r != 2 {
		t.Fatalf("persistGraph wrote %d relations, want 2 — both triples must persist", r)
	}
	n, err := ont.RelationCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("RelationCount = %d, want 2 — both triples must persist", n)
	}
}

// A triple asserted over an existing keyword edge upserts rather than erroring,
// and the keyword edge's stored id survives.
func TestPersistGraphUpsertsOverKeywordEdge(t *testing.T) {
	ont := persistStore(t)
	for _, e := range []ontology.Entity{
		{ID: "backpressure", Type: ontology.TypeTechnique, Name: "Backpressure"},
		{ID: "flow-control", Type: ontology.TypeConcept, Name: "Flow Control"},
	} {
		if err := ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	// The Pass-3 keyword shape: deterministic id, zero confidence, no evidence.
	if err := ont.AddRelation(ontology.Relation{
		ID:       "backpressure-extends-flow-control",
		SourceID: "backpressure", TargetID: "flow-control", Relation: ontology.RelExtends,
	}); err != nil {
		t.Fatal(err)
	}

	if e, r, _, _ := persistGraph(ont, twoNodeGraph(), nil, "raw/paper.pdf", "2026-01-01T00:00:00Z", temporalHooks{}); e != 2 || r != 1 {
		t.Fatalf("persistGraph wrote %d entities / %d relations, want 2/1", e, r)
	}
	rels, _ := ont.GetRelations("backpressure", ontology.Outbound, "")
	if len(rels) != 1 {
		t.Fatalf("relations = %d, want 1 (upsert, not a second row)", len(rels))
	}
	if rels[0].Evidence == "" {
		t.Error("the higher-confidence triple did not overwrite the keyword edge's empty evidence")
	}
	if rels[0].ID != "backpressure-extends-flow-control" {
		t.Errorf("stored id = %q, want the original keyword id preserved", rels[0].ID)
	}
}

// Type precedence rule 1: an entity that already exists keeps its stored type.
// The pass is a high-volume writer of unvalidated model output; it must never
// overwrite a type an article declared.
func TestPersistGraphNeverRetypesAnExistingEntity(t *testing.T) {
	ont := persistStore(t)
	if err := ont.AddEntity(ontology.Entity{
		ID: "backpressure", Type: ontology.TypeTechnique, Name: "Backpressure",
		ArticlePath: "wiki/concepts/backpressure.md",
	}); err != nil {
		t.Fatal(err)
	}

	g := twoNodeGraph()
	g.Entities[0].Type = ontology.TypeClaim // the model's guess, which must lose
	persistGraph(ont, g, nil, "raw/paper.pdf", "", temporalHooks{})

	e, _ := ont.GetEntity("backpressure")
	if e.Type != ontology.TypeTechnique {
		t.Errorf("Type = %q, want the stored %q kept", e.Type, ontology.TypeTechnique)
	}
	if e.ArticlePath != "wiki/concepts/backpressure.md" {
		t.Errorf("ArticlePath lost: %q", e.ArticlePath)
	}
	if e.Definition == "" {
		t.Error("description should still have landed on the existing row")
	}
}

// Type precedence rule 2: for a name in THIS run's concepts, use the derivation
// Pass 3 will use, so the two agree on the row they share.
func TestPersistGraphUsesConceptTypeForThisRunsConcepts(t *testing.T) {
	ont := persistStore(t)
	concepts := []ExtractedConcept{
		{Name: "backpressure", Type: ontology.TypeTechnique, Sources: []string{"raw/paper.pdf"}},
	}
	g := twoNodeGraph()
	g.Entities[0].Type = ontology.TypeClaim // model guess, overridden by the concept

	persistGraph(ont, g, concepts, "raw/paper.pdf", "", temporalHooks{})
	e, _ := ont.GetEntity("backpressure")
	if e.Type != ontology.TypeTechnique {
		t.Errorf("Type = %q, want Pass 3's derivation %q", e.Type, ontology.TypeTechnique)
	}
}

// Type precedence rule 3: an entity the pass invented keeps its normalized type.
func TestPersistGraphKeepsInventedEntityType(t *testing.T) {
	ont := persistStore(t)
	if e, r, _, _ := persistGraph(ont, twoNodeGraph(), nil, "raw/paper.pdf", "2026-01-01T00:00:00Z", temporalHooks{}); e != 2 || r != 1 {
		t.Fatalf("persistGraph wrote %d entities / %d relations, want 2/1", e, r)
	}
	e, _ := ont.GetEntity("backpressure")
	if e.Type != ontology.TypeTechnique {
		t.Errorf("Type = %q, want the extracted %q", e.Type, ontology.TypeTechnique)
	}
}

// failingLookupStore fails GetEntity and passes everything else through.
type failingLookupStore struct {
	store.OntologyStore
}

func (f failingLookupStore) GetEntity(string) (*store.Entity, error) {
	return nil, errors.New("database is locked")
}

// A GetEntity ERROR is not "absent". Falling through to the model's guessed
// type on a transient read failure would overwrite a type an article declared —
// the exact outcome rule 1 exists to prevent, in a data-mutating path.
func TestPersistGraphSkipsEntityOnLookupError(t *testing.T) {
	ont := persistStore(t)
	if err := ont.AddEntity(ontology.Entity{
		ID: "backpressure", Type: ontology.TypeTechnique, Name: "Backpressure",
	}); err != nil {
		t.Fatal(err)
	}

	g := twoNodeGraph()
	g.Entities[0].Type = ontology.TypeClaim // the guess that must not land

	entities, _, _, _ := persistGraph(failingLookupStore{ont}, g, nil, "raw/paper.pdf", "", temporalHooks{})
	if entities != 0 {
		t.Errorf("entities written = %d, want 0 — a lookup failure must skip, not guess", entities)
	}

	e, err := ont.GetEntity("backpressure")
	if err != nil || e == nil {
		t.Fatalf("GetEntity: %v %v", e, err)
	}
	if e.Type != ontology.TypeTechnique {
		t.Errorf("Type = %q, want the stored %q — a read failure overwrote a declared type",
			e.Type, ontology.TypeTechnique)
	}
}
