package ontology

import "testing"

func TestAllRelations(t *testing.T) {
	s := setupTestDB(t)
	s.AddEntity(Entity{ID: "e1", Type: "concept", Name: "One"})
	s.AddEntity(Entity{ID: "e2", Type: "concept", Name: "Two"})
	s.AddEntity(Entity{ID: "e3", Type: "concept", Name: "Three"})
	s.AddRelation(Relation{ID: "r1", SourceID: "e1", TargetID: "e2", Relation: RelImplements})
	s.AddRelation(Relation{ID: "r2", SourceID: "e2", TargetID: "e3", Relation: "contradicts"})

	all, err := s.AllRelations()
	if err != nil {
		t.Fatalf("AllRelations: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("AllRelations len = %d, want 2", len(all))
	}
	// Fully populated rows (spec §3: SELECT * semantics, not the 3-column web shape).
	seen := map[string]Relation{}
	for _, r := range all {
		seen[r.SourceID+"→"+r.TargetID] = r
	}
	if r := seen["e1→e2"]; r.Relation != RelImplements {
		t.Errorf("relation e1→e2 = %+v", r)
	}
}

func TestRelationsByType(t *testing.T) {
	s := setupTestDB(t)
	s.AddEntity(Entity{ID: "e1", Type: "concept", Name: "One"})
	s.AddEntity(Entity{ID: "e2", Type: "concept", Name: "Two"})
	s.AddEntity(Entity{ID: "e3", Type: "concept", Name: "Three"})
	s.AddRelation(Relation{ID: "r4", SourceID: "e1", TargetID: "e2", Relation: "contradicts"})
	s.AddRelation(Relation{ID: "r5", SourceID: "e2", TargetID: "e3", Relation: RelImplements})
	s.AddRelation(Relation{ID: "r6", SourceID: "e3", TargetID: "e1", Relation: "contradicts"})

	rels, err := s.RelationsByType("contradicts")
	if err != nil {
		t.Fatalf("RelationsByType: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("RelationsByType len = %d, want 2", len(rels))
	}
	for _, r := range rels {
		if r.Relation != "contradicts" {
			t.Errorf("unexpected relation type: %+v", r)
		}
	}
}

func TestEntityConnectionCounts(t *testing.T) {
	s := setupTestDB(t)
	s.AddEntity(Entity{ID: "hub", Type: "concept", Name: "Hub"})
	s.AddEntity(Entity{ID: "a", Type: "concept", Name: "A"})
	s.AddEntity(Entity{ID: "b", Type: "concept", Name: "B"})
	s.AddEntity(Entity{ID: "isolated", Type: "concept", Name: "Iso"})
	s.AddRelation(Relation{ID: "r7", SourceID: "hub", TargetID: "a", Relation: RelImplements})
	s.AddRelation(Relation{ID: "r8", SourceID: "b", TargetID: "hub", Relation: RelExtends})

	counts, err := s.EntityConnectionCounts()
	if err != nil {
		t.Fatalf("EntityConnectionCounts: %v", err)
	}
	// PARITY (web/server.go:712): the absorbed query's outer GROUP BY id has no
	// SUM aggregate, so dual-side entities report ONE side's count, not the
	// total — hub with 2 connections reports 1. Latent bug reproduced
	// byte-for-byte per zero-behavior-change; fix deferred (decisions.md).
	if counts["hub"] != 1 {
		t.Errorf("hub connections = %d, want 1 (parity with absorbed query)", counts["hub"])
	}
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Errorf("leaf counts wrong: %+v", counts)
	}
	if counts["isolated"] != 0 {
		t.Errorf("isolated should have no entry, got %d", counts["isolated"])
	}
}
