package ontology

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// P3-6: bi-temporal edge validity — default live-at-now filtering,
// point-in-time reads, TraverseOpts.AsOf, and the enabled gate.

const (
	tPast   = "2020-01-01T00:00:00Z"
	tMid    = "2025-06-01T00:00:00Z"
	tFuture = "2099-01-01T00:00:00Z"
)

func addRel(t *testing.T, s *Store, r Relation) {
	t.Helper()
	if err := s.AddRelation(r); err != nil {
		t.Fatalf("AddRelation(%s -%s-> %s): %v", r.SourceID, r.Relation, r.TargetID, err)
	}
}

func temporalSeed(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"a", "b", "c", "d"} {
		if err := s.AddEntity(Entity{ID: id, Type: TypeConcept, Name: id}); err != nil {
			t.Fatalf("AddEntity(%s): %v", id, err)
		}
	}
	// live: no temporal fields at all (legacy)
	addRel(t, s, Relation{ID: "r-live", SourceID: "a", TargetID: "b", Relation: RelExtends, Confidence: 0.9})
	// dead: valid_to in the past
	addRel(t, s, Relation{ID: "r-dead", SourceID: "a", TargetID: "c", Relation: RelExtends, Confidence: 0.9,
		ValidFrom: tPast, ValidTo: tMid, InvalidatedBy: "r-live"})
	// future: valid_from in the future — not live yet
	addRel(t, s, Relation{ID: "r-future", SourceID: "a", TargetID: "d", Relation: RelExtends, Confidence: 0.9,
		ValidFrom: tFuture})
}

func relIDs(rels []Relation) map[string]bool {
	out := make(map[string]bool, len(rels))
	for _, r := range rels {
		out[r.ID] = true
	}
	return out
}

func TestGetRelationsDefaultFiltersToLive(t *testing.T) {
	s := setupTestDB(t)
	temporalSeed(t, s)

	rels, err := s.GetRelations("a", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := relIDs(rels)
	if !ids["r-live"] {
		t.Error("default read must return the legacy (no-temporal) edge")
	}
	if ids["r-dead"] {
		t.Error("default read must NOT return an edge whose valid_to is past")
	}
	if ids["r-future"] {
		t.Error("default read must NOT return an edge whose valid_from is future")
	}
}

func TestGetRelationsAtPointInTime(t *testing.T) {
	s := setupTestDB(t)
	temporalSeed(t, s)

	asOf, _ := time.Parse(time.RFC3339, "2022-01-01T00:00:00Z")
	rels, err := s.GetRelationsAt("a", Outbound, "", asOf)
	if err != nil {
		t.Fatal(err)
	}
	ids := relIDs(rels)
	if !ids["r-live"] || !ids["r-dead"] {
		t.Errorf("as_of 2022 must return r-live and r-dead, got %v", ids)
	}
	if ids["r-future"] {
		t.Error("as_of 2022 must NOT return the 2099 edge")
	}

	// boundary: exactly at valid_to → NOT live (strict >)
	edge, _ := time.Parse(time.RFC3339, tMid)
	rels, err = s.GetRelationsAt("a", Outbound, "", edge)
	if err != nil {
		t.Fatal(err)
	}
	if relIDs(rels)["r-dead"] {
		t.Error("as_of == valid_to must NOT be live (strict >)")
	}
}

func TestGetRelationsZeroAsOfEqualsDefault(t *testing.T) {
	s := setupTestDB(t)
	temporalSeed(t, s)
	rels, err := s.GetRelationsAt("a", Outbound, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].ID != "r-live" {
		t.Errorf("zero asOf must behave as live-at-now, got %v", relIDs(rels))
	}
}

func TestRelationsByTypeFiltersToLive(t *testing.T) {
	s := setupTestDB(t)
	temporalSeed(t, s)
	rels, err := s.RelationsByType(RelExtends)
	if err != nil {
		t.Fatal(err)
	}
	ids := relIDs(rels)
	if !ids["r-live"] || ids["r-dead"] || ids["r-future"] {
		t.Errorf("RelationsByType default must be live-only, got %v", ids)
	}
}

func TestTraverseHonorsAsOf(t *testing.T) {
	s := setupTestDB(t)
	temporalSeed(t, s)

	now, err := s.Traverse("a", TraverseOpts{Direction: Outbound, MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(now) != 1 || now[0].ID != "b" {
		t.Errorf("default traverse must reach only b, got %+v", now)
	}

	asOf, _ := time.Parse(time.RFC3339, "2022-01-01T00:00:00Z")
	then, err := s.Traverse("a", TraverseOpts{Direction: Outbound, MaxDepth: 1, AsOf: asOf})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range then {
		got[e.ID] = true
	}
	if !got["b"] || !got["c"] || len(then) != 2 {
		t.Errorf("as_of traverse must reach b and c, got %v", got)
	}
}

func TestDerivedArmFiltered(t *testing.T) {
	s := setupTestDB(t)
	for _, id := range []string{"canon", "als", "x"} {
		if err := s.AddEntity(Entity{ID: id, Type: TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// alias entity holds a dead edge; linking derives a copy onto canon.
	addRel(t, s, Relation{ID: "r-alias-dead", SourceID: "als", TargetID: "x", Relation: RelExtends,
		Confidence: 0.9, ValidFrom: tPast, ValidTo: tMid})
	if err := s.PutAlias(mkAlias("als", "canon", store.AliasApplied)); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetActiveAlias("als")
	if err != nil || a == nil {
		t.Fatalf("GetActiveAlias: %v %v", a, err)
	}
	if _, err := s.LinkAlias(*a); err != nil {
		t.Fatal(err)
	}

	rels, err := s.GetRelations("canon", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if r.TargetID == "x" {
			t.Errorf("default read must not surface the derived copy of a dead edge: %+v", r)
		}
	}

	asOf, _ := time.Parse(time.RFC3339, "2022-01-01T00:00:00Z")
	rels, err = s.GetRelationsAt("canon", Outbound, "", asOf)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rels {
		if r.TargetID == "x" {
			found = true
		}
	}
	if !found {
		t.Error("as_of read must surface the derived copy when it was valid")
	}
}

func TestTemporalDisabledPassesEverythingThrough(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewStore(db, ValidRelationNames(BuiltinRelations), ValidEntityTypeNames(BuiltinEntityTypes),
		WithTemporalEnabled(false))
	temporalSeed(t, s)

	rels, err := s.GetRelations("a", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := relIDs(rels)
	if !ids["r-live"] || !ids["r-dead"] || !ids["r-future"] {
		t.Errorf("temporal disabled must return all edges, got %v", ids)
	}

	rels, err = s.RelationsByType(RelExtends)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 3 {
		t.Errorf("disabled RelationsByType must return all 3, got %d", len(rels))
	}
}

func TestTemporalEnabledByDefault(t *testing.T) {
	s := setupTestDB(t) // no option passed
	temporalSeed(t, s)
	rels, err := s.GetRelations("a", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("absence of option must mean enabled (default), got %v", relIDs(rels))
	}
}

func TestInvalidateFunctionalDisabledNoOp(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewStore(db, ValidRelationNames(BuiltinRelations), ValidEntityTypeNames(BuiltinEntityTypes),
		WithTemporalEnabled(false))
	temporalSeed(t, s)
	ids, err := s.InvalidateFunctional("a", RelExtends, "b", "", "x")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil {
		t.Errorf("disabled store must no-op, got %v", ids)
	}
	rels, _ := s.GetRelations("a", Outbound, "")
	if len(rels) != 3 {
		t.Errorf("disabled store must leave all edges live, got %d", len(rels))
	}
}
