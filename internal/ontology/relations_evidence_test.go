package ontology

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// evidenceStore builds an ontology store over a real SQLite DB with three
// entities wired up, matching the repo's t.TempDir + storage.Open convention.
func evidenceStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewStore(db, nil, nil)
	for _, e := range []Entity{
		{ID: "alpha", Type: TypeConcept, Name: "Alpha"},
		{ID: "beta", Type: TypeConcept, Name: "Beta"},
		{ID: "gamma", Type: TypeConcept, Name: "Gamma"},
	} {
		if err := s.AddEntity(e); err != nil {
			t.Fatalf("seed entity %s: %v", e.ID, err)
		}
	}
	return s
}

func assertEvidenced(t *testing.T, where string, r Relation) {
	t.Helper()
	if r.Evidence != "alpha extends beta because reasons" {
		t.Errorf("%s: Evidence = %q", where, r.Evidence)
	}
	if r.Confidence != 0.8 {
		t.Errorf("%s: Confidence = %v, want 0.8", where, r.Confidence)
	}
	if r.SourceDoc != "raw/paper.pdf" {
		t.Errorf("%s: SourceDoc = %q", where, r.SourceDoc)
	}
	if r.ValidFrom != "2026-01-15T00:00:00Z" || r.ValidTo != "2099-06-01T00:00:00Z" || r.InvalidatedBy != "later-edge" {
		t.Errorf("%s: temporal fields = %q/%q/%q", where, r.ValidFrom, r.ValidTo, r.InvalidatedBy)
	}
}

// (a) An evidenced relation must round-trip through EVERY read path — all
// three GetRelations directions included. The three direction branches wrote
// their column lists separately before P3-1, which is exactly how Inbound and
// Both would come to return zero-valued evidence while Outbound looked fine.
func TestRelationEvidenceRoundTripsAllReadPaths(t *testing.T) {
	s := evidenceStore(t)

	in := Relation{
		ID: "alpha-extends-beta", SourceID: "alpha", TargetID: "beta", Relation: RelExtends,
		Evidence: "alpha extends beta because reasons", Confidence: 0.8, SourceDoc: "raw/paper.pdf",
		// P3-6: values are RFC3339 (writers normalize; date-only strings would
		// sort below their own midnight — a lexical accident, not semantics)
		// and ValidTo is far-future so the edge stays live under the default
		// validity filter that GetRelations/RelationsByType now apply.
		ValidFrom: "2026-01-15T00:00:00Z", ValidTo: "2099-06-01T00:00:00Z", InvalidatedBy: "later-edge",
	}
	if err := s.AddRelation(in); err != nil {
		t.Fatalf("AddRelation: %v", err)
	}

	readPaths := []struct {
		name string
		read func() ([]Relation, error)
	}{
		{"GetRelations/Outbound", func() ([]Relation, error) { return s.GetRelations("alpha", Outbound, "") }},
		{"GetRelations/Inbound", func() ([]Relation, error) { return s.GetRelations("beta", Inbound, "") }},
		{"GetRelations/Both", func() ([]Relation, error) { return s.GetRelations("alpha", Both, "") }},
		{"ListRelations/all", func() ([]Relation, error) { return s.ListRelations("", 10) }},
		{"ListRelations/typed", func() ([]Relation, error) { return s.ListRelations(RelExtends, 10) }},
		{"AllRelations", s.AllRelations},
		{"RelationsByType", func() ([]Relation, error) { return s.RelationsByType(RelExtends) }},
	}
	for _, tc := range readPaths {
		rels, err := tc.read()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(rels) != 1 {
			t.Fatalf("%s: got %d relations, want 1", tc.name, len(rels))
		}
		assertEvidenced(t, tc.name, rels[0])
	}
}

// (b) A caller that sets none of the new fields — i.e. every caller that
// existed before P3-1 — still works and reads back zero-valued. This is the
// back-compat half of the schema change.
func TestRelationLegacyCallerReadsBackZeroValued(t *testing.T) {
	s := evidenceStore(t)

	if err := s.AddRelation(Relation{
		ID: "alpha-cites-beta", SourceID: "alpha", TargetID: "beta", Relation: RelCites,
	}); err != nil {
		t.Fatalf("AddRelation: %v", err)
	}

	rels, err := s.GetRelations("alpha", Outbound, "")
	if err != nil {
		t.Fatalf("GetRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relations, want 1", len(rels))
	}
	r := rels[0]
	if r.Evidence != "" || r.Confidence != 0 || r.SourceDoc != "" {
		t.Errorf("evidence fields not zero: %q / %v / %q", r.Evidence, r.Confidence, r.SourceDoc)
	}
	if r.ValidFrom != "" || r.ValidTo != "" || r.InvalidatedBy != "" {
		t.Errorf("temporal fields not zero: %q / %q / %q", r.ValidFrom, r.ValidTo, r.InvalidatedBy)
	}
	if r.CreatedAt == "" {
		t.Error("CreatedAt should still be defaulted")
	}
}

// (e) D11 regression. `WHERE source_id=? OR target_id=?` with `AND relation=?`
// appended parses as `source_id=? OR (target_id=? AND relation=?)`, so a
// Both-direction query with a relation filter returned every outbound edge
// regardless of type. Postgres parenthesized correctly and SQLite did not.
func TestGetRelationsBothHonorsRelationFilterOnBothSides(t *testing.T) {
	s := evidenceStore(t)

	// beta is the hub: one inbound `extends`, one outbound `cites`.
	// A filtered Both query on beta must see exactly the `extends` edge.
	for _, r := range []Relation{
		{ID: "alpha-extends-beta", SourceID: "alpha", TargetID: "beta", Relation: RelExtends},
		{ID: "beta-cites-gamma", SourceID: "beta", TargetID: "gamma", Relation: RelCites},
	} {
		if err := s.AddRelation(r); err != nil {
			t.Fatalf("AddRelation %s: %v", r.ID, err)
		}
	}

	rels, err := s.GetRelations("beta", Both, RelExtends)
	if err != nil {
		t.Fatalf("GetRelations: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relations, want 1 (the outbound `cites` edge must be filtered out)", len(rels))
	}
	if rels[0].Relation != RelExtends {
		t.Errorf("Relation = %q, want %q", rels[0].Relation, RelExtends)
	}

	// The mirror case: filtering on the outbound type must exclude the inbound one.
	rels, err = s.GetRelations("beta", Both, RelCites)
	if err != nil {
		t.Fatalf("GetRelations: %v", err)
	}
	if len(rels) != 1 || rels[0].Relation != RelCites {
		t.Fatalf("cites filter returned %d relations, want exactly the cites edge", len(rels))
	}
}

// (c) The upsert rule. A re-asserted edge overwrites evidence ONLY when the new
// confidence is strictly higher. The guard is what makes this change
// back-compatible: every caller that existed before P3-1 passes Confidence 0,
// so `0 > COALESCE(stored,0)` is false and the statement is a no-op —
// bit-identical to the DO NOTHING it replaces.
func TestAddRelationUpsertOnlyOnHigherConfidence(t *testing.T) {
	s := evidenceStore(t)

	base := Relation{
		ID: "alpha-extends-beta", SourceID: "alpha", TargetID: "beta", Relation: RelExtends,
		Evidence: "first", Confidence: 0.4, SourceDoc: "raw/first.md",
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	if err := s.AddRelation(base); err != nil {
		t.Fatalf("seed: %v", err)
	}

	read := func() Relation {
		t.Helper()
		rels, err := s.GetRelations("alpha", Outbound, RelExtends)
		if err != nil || len(rels) != 1 {
			t.Fatalf("read back: %+v %v", rels, err)
		}
		return rels[0]
	}

	// Higher confidence wins, and keeps the ORIGINAL created_at — the earliest
	// assertion's timestamp is the edge's birthday, not its last touch.
	higher := base
	higher.Evidence = "better"
	higher.Confidence = 0.9
	higher.SourceDoc = "raw/second.md"
	higher.CreatedAt = "2026-05-05T00:00:00Z"
	if err := s.AddRelation(higher); err != nil {
		t.Fatalf("upsert higher: %v", err)
	}
	got := read()
	if got.Evidence != "better" || got.Confidence != 0.9 || got.SourceDoc != "raw/second.md" {
		t.Errorf("higher confidence did not win: %+v", got)
	}
	if got.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("created_at = %q, want the original preserved", got.CreatedAt)
	}

	// Lower confidence is a no-op.
	lower := base
	lower.Evidence = "worse"
	lower.Confidence = 0.2
	if err := s.AddRelation(lower); err != nil {
		t.Fatalf("upsert lower: %v", err)
	}
	if got := read(); got.Evidence != "better" || got.Confidence != 0.9 {
		t.Errorf("lower confidence overwrote: %+v", got)
	}

	// Zero confidence over a stored 0.9 is a no-op. THIS is the case that
	// matters in production: the Pass-3 keyword extractor re-asserts the same
	// (source,target,relation) on every compile with Confidence 0, and it must
	// never erase an LLM-extracted edge's evidence.
	keyword := Relation{
		ID: "alpha-extends-beta", SourceID: "alpha", TargetID: "beta", Relation: RelExtends,
	}
	if err := s.AddRelation(keyword); err != nil {
		t.Fatalf("keyword re-assert: %v", err)
	}
	if got := read(); got.Evidence != "better" || got.Confidence != 0.9 || got.SourceDoc != "raw/second.md" {
		t.Errorf("zero-confidence re-assertion erased evidence: %+v", got)
	}
}

// (d) AddEntity guards: an empty incoming field never clobbers a stored value,
// while `type` is written unconditionally so a wrong type stays correctable.
func TestAddEntityDoesNotClobberWithEmpty(t *testing.T) {
	s := evidenceStore(t)

	full := Entity{
		ID: "delta", Type: TypeTechnique, Name: "Delta",
		Definition: "a technique for deltas", ArticlePath: "wiki/concepts/delta.md",
	}
	if err := s.AddEntity(full); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The Pass-3 shape: same ID, no Definition. Before P3-1 this erased the
	// definition — which is exactly the description P3-3 needs.
	if err := s.AddEntity(Entity{ID: "delta", Type: TypeTechnique, Name: "Delta"}); err != nil {
		t.Fatalf("re-add without definition: %v", err)
	}
	got, err := s.GetEntity("delta")
	if err != nil || got == nil {
		t.Fatalf("GetEntity: %v %v", got, err)
	}
	if got.Definition != "a technique for deltas" {
		t.Errorf("definition clobbered by empty: %q", got.Definition)
	}
	if got.ArticlePath != "wiki/concepts/delta.md" {
		t.Errorf("article_path clobbered by empty: %q", got.ArticlePath)
	}
	if got.Name != "Delta" {
		t.Errorf("name = %q", got.Name)
	}

	// A non-empty value still overwrites — the guard blocks erasure, not updates.
	if err := s.AddEntity(Entity{
		ID: "delta", Type: TypeTechnique, Name: "Delta Prime", Definition: "revised",
	}); err != nil {
		t.Fatalf("re-add with values: %v", err)
	}
	got, _ = s.GetEntity("delta")
	if got.Definition != "revised" || got.Name != "Delta Prime" {
		t.Errorf("non-empty values did not overwrite: %+v", got)
	}
	if got.ArticlePath != "wiki/concepts/delta.md" {
		t.Errorf("article_path lost on a write that omitted it: %q", got.ArticlePath)
	}
}

// `type` is written unconditionally, in BOTH directions. SQLite had no type
// clause at all before P3-1, so a wrong type was permanent; a guarded clause
// (treating "concept" as "no information") would have made
// `ontology add --entity-type concept` a silent no-op that still reported
// success.
func TestAddEntityTypeIsCorrectableInBothDirections(t *testing.T) {
	s := evidenceStore(t)

	if err := s.AddEntity(Entity{ID: "epsilon", Type: TypeConcept, Name: "Epsilon"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.AddEntity(Entity{ID: "epsilon", Type: TypeTechnique, Name: "Epsilon"}); err != nil {
		t.Fatalf("retype up: %v", err)
	}
	got, _ := s.GetEntity("epsilon")
	if got.Type != TypeTechnique {
		t.Errorf("type = %q, want %q", got.Type, TypeTechnique)
	}

	// And back to the default type — the direction a guarded clause would have
	// made unreachable.
	if err := s.AddEntity(Entity{ID: "epsilon", Type: TypeConcept, Name: "Epsilon"}); err != nil {
		t.Fatalf("retype back: %v", err)
	}
	got, _ = s.GetEntity("epsilon")
	if got.Type != TypeConcept {
		t.Errorf("type = %q, want %q back", got.Type, TypeConcept)
	}
}
