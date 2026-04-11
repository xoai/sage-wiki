package ontology

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, ValidRelationNames(BuiltinRelations), ValidEntityTypeNames(BuiltinEntityTypes))
}

func TestAddAndGetEntity(t *testing.T) {
	store := setupTestDB(t)

	err := store.AddEntity(Entity{
		ID:          "self-attention",
		Type:        TypeConcept,
		Name:        "Self-Attention",
		Definition:  "A mechanism for computing contextual representations",
		ArticlePath: "wiki/concepts/self-attention.md",
	})
	if err != nil {
		t.Fatalf("AddEntity: %v", err)
	}

	e, err := store.GetEntity("self-attention")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if e == nil {
		t.Fatal("expected entity, got nil")
	}
	if e.Name != "Self-Attention" {
		t.Errorf("expected Self-Attention, got %q", e.Name)
	}
	if e.Type != TypeConcept {
		t.Errorf("expected concept, got %q", e.Type)
	}
}

func TestListEntities(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeTechnique, Name: "B"})
	store.AddEntity(Entity{ID: "e3", Type: TypeConcept, Name: "C"})

	// All
	all, _ := store.ListEntities("")
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	// Filter by type
	concepts, _ := store.ListEntities(TypeConcept)
	if len(concepts) != 2 {
		t.Errorf("expected 2 concepts, got %d", len(concepts))
	}
}

func TestDeleteEntity(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.DeleteEntity("e1")

	e, _ := store.GetEntity("e1")
	if e != nil {
		t.Error("expected nil after delete")
	}
}

func TestAddRelation(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "flash-attn", Type: TypeTechnique, Name: "Flash Attention"})
	store.AddEntity(Entity{ID: "attention", Type: TypeConcept, Name: "Attention"})

	err := store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "flash-attn",
		TargetID: "attention",
		Relation: RelImplements,
	})
	if err != nil {
		t.Fatalf("AddRelation: %v", err)
	}

	rels, err := store.GetRelations("flash-attn", Outbound, "")
	if err != nil {
		t.Fatalf("GetRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(rels))
	}
	if rels[0].Relation != RelImplements {
		t.Errorf("expected implements, got %q", rels[0].Relation)
	}
}

func TestUnknownRelationRejected(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeConcept, Name: "B"})

	err := store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "e1",
		TargetID: "e2",
		Relation: "unknown_type",
	})
	if err == nil {
		t.Error("expected error for unknown relation type")
	}
}

func TestCustomRelationAccepted(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Include a custom relation type
	validNames := append(ValidRelationNames(BuiltinRelations), "regulates")
	store := NewStore(db, validNames, ValidEntityTypeNames(BuiltinEntityTypes))

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeConcept, Name: "B"})

	err = store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "e1",
		TargetID: "e2",
		Relation: "regulates",
	})
	if err != nil {
		t.Fatalf("expected custom relation to be accepted: %v", err)
	}
}

func TestNilValidRelationsAcceptsAll(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := NewStore(db, nil, nil)
	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeConcept, Name: "B"})

	err = store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "e1",
		TargetID: "e2",
		Relation: "anything_goes",
	})
	if err != nil {
		t.Fatalf("nil validRelations should accept all: %v", err)
	}
}

func TestSelfLoopRejected(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})

	err := store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "e1",
		TargetID: "e1",
		Relation: RelExtends,
	})
	if err == nil {
		t.Error("expected error for self-loop")
	}
}

func TestUpsertRelation(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeConcept, Name: "B"})

	// Insert same relation twice — should not error (upsert)
	store.AddRelation(Relation{ID: "r1", SourceID: "e1", TargetID: "e2", Relation: RelExtends})
	err := store.AddRelation(Relation{ID: "r2", SourceID: "e1", TargetID: "e2", Relation: RelExtends})
	if err != nil {
		t.Fatalf("upsert should not error: %v", err)
	}

	count, _ := store.RelationCount()
	if count != 1 {
		t.Errorf("expected 1 relation (upsert), got %d", count)
	}
}

func TestTraverseBFS(t *testing.T) {
	store := setupTestDB(t)

	// Build a graph: A -> B -> C
	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})
	store.AddEntity(Entity{ID: "c", Type: TypeConcept, Name: "C"})
	store.AddRelation(Relation{ID: "r1", SourceID: "a", TargetID: "b", Relation: RelPrerequisiteOf})
	store.AddRelation(Relation{ID: "r2", SourceID: "b", TargetID: "c", Relation: RelPrerequisiteOf})

	// Depth 1: should find B only
	result, err := store.Traverse("a", TraverseOpts{Direction: Outbound, MaxDepth: 1})
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(result) != 1 || result[0].ID != "b" {
		t.Errorf("depth 1: expected [b], got %v", entityIDs(result))
	}

	// Depth 2: should find B and C
	result, _ = store.Traverse("a", TraverseOpts{Direction: Outbound, MaxDepth: 2})
	if len(result) != 2 {
		t.Errorf("depth 2: expected 2 entities, got %d", len(result))
	}
}

func TestTraverseWithRelationFilter(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})
	store.AddEntity(Entity{ID: "c", Type: TypeConcept, Name: "C"})
	store.AddRelation(Relation{ID: "r1", SourceID: "a", TargetID: "b", Relation: RelPrerequisiteOf})
	store.AddRelation(Relation{ID: "r2", SourceID: "a", TargetID: "c", Relation: RelOptimizes})

	// Filter by prerequisite_of only
	result, _ := store.Traverse("a", TraverseOpts{Direction: Outbound, RelationType: RelPrerequisiteOf, MaxDepth: 1})
	if len(result) != 1 || result[0].ID != "b" {
		t.Errorf("expected [b] with filter, got %v", entityIDs(result))
	}
}

func TestDetectCycles(t *testing.T) {
	store := setupTestDB(t)

	// A -> B -> C -> A (cycle)
	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})
	store.AddEntity(Entity{ID: "c", Type: TypeConcept, Name: "C"})
	store.AddRelation(Relation{ID: "r1", SourceID: "a", TargetID: "b", Relation: RelPrerequisiteOf})
	store.AddRelation(Relation{ID: "r2", SourceID: "b", TargetID: "c", Relation: RelPrerequisiteOf})
	store.AddRelation(Relation{ID: "r3", SourceID: "c", TargetID: "a", Relation: RelPrerequisiteOf})

	cycles, err := store.DetectCycles("a")
	if err != nil {
		t.Fatalf("DetectCycles: %v", err)
	}
	if len(cycles) == 0 {
		t.Error("expected cycle, found none")
	}
	// Cycle should include a -> b -> c -> a
	if len(cycles[0]) != 4 {
		t.Errorf("expected cycle length 4, got %d: %v", len(cycles[0]), cycles[0])
	}
}

func TestNoCycle(t *testing.T) {
	store := setupTestDB(t)

	// A -> B -> C (no cycle)
	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})
	store.AddEntity(Entity{ID: "c", Type: TypeConcept, Name: "C"})
	store.AddRelation(Relation{ID: "r1", SourceID: "a", TargetID: "b", Relation: RelPrerequisiteOf})
	store.AddRelation(Relation{ID: "r2", SourceID: "b", TargetID: "c", Relation: RelPrerequisiteOf})

	cycles, _ := store.DetectCycles("a")
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, found %d", len(cycles))
	}
}

func TestEntityAndRelationCount(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeTechnique, Name: "B"})

	total, _ := store.EntityCount("")
	if total != 2 {
		t.Errorf("expected 2 total, got %d", total)
	}

	concepts, _ := store.EntityCount(TypeConcept)
	if concepts != 1 {
		t.Errorf("expected 1 concept, got %d", concepts)
	}

	store.AddRelation(Relation{ID: "r1", SourceID: "e1", TargetID: "e2", Relation: RelImplements})
	relCount, _ := store.RelationCount()
	if relCount != 1 {
		t.Errorf("expected 1 relation, got %d", relCount)
	}
}

func TestCascadeDelete(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "e1", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "e2", Type: TypeConcept, Name: "B"})
	store.AddRelation(Relation{ID: "r1", SourceID: "e1", TargetID: "e2", Relation: RelExtends})

	// Delete e1 — should cascade to relations
	store.DeleteEntity("e1")

	relCount, _ := store.RelationCount()
	if relCount != 0 {
		t.Errorf("expected 0 relations after cascade delete, got %d", relCount)
	}
}

func TestEntityDegree(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})
	store.AddEntity(Entity{ID: "c", Type: TypeConcept, Name: "C"})
	store.AddRelation(Relation{ID: "r1", SourceID: "a", TargetID: "b", Relation: RelExtends})
	store.AddRelation(Relation{ID: "r2", SourceID: "c", TargetID: "a", Relation: RelImplements})

	deg, err := store.EntityDegree("a")
	if err != nil {
		t.Fatalf("EntityDegree: %v", err)
	}
	if deg != 2 {
		t.Errorf("expected degree 2 (1 outbound + 1 inbound), got %d", deg)
	}

	deg, _ = store.EntityDegree("b")
	if deg != 1 {
		t.Errorf("expected degree 1, got %d", deg)
	}

	// Nonexistent entity
	deg, _ = store.EntityDegree("nonexistent")
	if deg != 0 {
		t.Errorf("expected degree 0 for nonexistent, got %d", deg)
	}
}

func TestEntitiesCiting(t *testing.T) {
	store := setupTestDB(t)

	// Two concepts cite the same source
	store.AddEntity(Entity{ID: "attention", Type: TypeConcept, Name: "Attention", ArticlePath: "wiki/concepts/attention.md"})
	store.AddEntity(Entity{ID: "transformer", Type: TypeConcept, Name: "Transformer", ArticlePath: "wiki/concepts/transformer.md"})
	store.AddEntity(Entity{ID: "raw/paper.pdf", Type: TypeSource, Name: "paper.pdf"})

	store.AddRelation(Relation{ID: "c1", SourceID: "attention", TargetID: "raw/paper.pdf", Relation: RelCites})
	store.AddRelation(Relation{ID: "c2", SourceID: "transformer", TargetID: "raw/paper.pdf", Relation: RelCites})

	entities, err := store.EntitiesCiting("raw/paper.pdf")
	if err != nil {
		t.Fatalf("EntitiesCiting: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("expected 2 citing entities, got %d", len(entities))
	}

	// Non-cites relation should not appear
	store.AddEntity(Entity{ID: "lstm", Type: TypeConcept, Name: "LSTM"})
	store.AddRelation(Relation{ID: "r1", SourceID: "lstm", TargetID: "raw/paper.pdf", Relation: RelExtends})

	entities, _ = store.EntitiesCiting("raw/paper.pdf")
	if len(entities) != 2 {
		t.Errorf("expected 2 (non-cites excluded), got %d", len(entities))
	}
}

func TestCitedBy(t *testing.T) {
	store := setupTestDB(t)

	store.AddEntity(Entity{ID: "attention", Type: TypeConcept, Name: "Attention"})
	store.AddEntity(Entity{ID: "raw/paper.pdf", Type: TypeSource, Name: "paper.pdf"})
	store.AddEntity(Entity{ID: "raw/notes.md", Type: TypeSource, Name: "notes.md"})

	store.AddRelation(Relation{ID: "c1", SourceID: "attention", TargetID: "raw/paper.pdf", Relation: RelCites})
	store.AddRelation(Relation{ID: "c2", SourceID: "attention", TargetID: "raw/notes.md", Relation: RelCites})

	sources, err := store.CitedBy("attention")
	if err != nil {
		t.Fatalf("CitedBy: %v", err)
	}
	if len(sources) != 2 {
		t.Errorf("expected 2 cited sources, got %d", len(sources))
	}

	// Nonexistent entity
	sources, _ = store.CitedBy("nonexistent")
	if len(sources) != 0 {
		t.Errorf("expected 0 for nonexistent, got %d", len(sources))
	}
}

func entityIDs(entities []Entity) []string {
	ids := make([]string, len(entities))
	for i, e := range entities {
		ids[i] = e.ID
	}
	return ids
}
