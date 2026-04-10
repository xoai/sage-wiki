package ontology

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
)

func TestMergedRelationsEmptyConfig(t *testing.T) {
	merged := MergedRelations(nil)
	if len(merged) != len(BuiltinRelations) {
		t.Fatalf("expected %d builtins, got %d", len(BuiltinRelations), len(merged))
	}
	// Verify builtins are intact
	for i, b := range BuiltinRelations {
		if merged[i].Name != b.Name {
			t.Errorf("index %d: expected %q, got %q", i, b.Name, merged[i].Name)
		}
	}
}

func TestMergedRelationsExtendBuiltin(t *testing.T) {
	cfg := []config.RelationConfig{
		{Name: "implements", Synonyms: []string{"thực hiện", "triển khai"}},
	}
	merged := MergedRelations(cfg)

	// Should still be 8 total (no new types)
	if len(merged) != len(BuiltinRelations) {
		t.Fatalf("expected %d, got %d", len(BuiltinRelations), len(merged))
	}

	// Find implements
	var implDef RelationDef
	for _, d := range merged {
		if d.Name == RelImplements {
			implDef = d
			break
		}
	}

	// Should have original synonyms + 2 new ones
	originalCount := len(BuiltinRelations[0].Synonyms)
	expectedCount := originalCount + 2
	if len(implDef.Synonyms) != expectedCount {
		t.Errorf("expected %d synonyms, got %d: %v", expectedCount, len(implDef.Synonyms), implDef.Synonyms)
	}

	// Check new synonyms are present
	found := map[string]bool{}
	for _, s := range implDef.Synonyms {
		found[s] = true
	}
	if !found["thực hiện"] || !found["triển khai"] {
		t.Error("expected Vietnamese synonyms to be appended")
	}
}

func TestMergedRelationsAddCustom(t *testing.T) {
	cfg := []config.RelationConfig{
		{Name: "regulates", Synonyms: []string{"regulates", "regulated by", "调控"}},
	}
	merged := MergedRelations(cfg)

	if len(merged) != len(BuiltinRelations)+1 {
		t.Fatalf("expected %d, got %d", len(BuiltinRelations)+1, len(merged))
	}

	last := merged[len(merged)-1]
	if last.Name != "regulates" {
		t.Errorf("expected regulates, got %q", last.Name)
	}
	if len(last.Synonyms) != 3 {
		t.Errorf("expected 3 synonyms, got %d", len(last.Synonyms))
	}
}

func TestMergedRelationsDedupSynonyms(t *testing.T) {
	cfg := []config.RelationConfig{
		// "implements" is already a built-in synonym
		{Name: "implements", Synonyms: []string{"implements", "new_keyword"}},
	}
	merged := MergedRelations(cfg)

	var implDef RelationDef
	for _, d := range merged {
		if d.Name == RelImplements {
			implDef = d
			break
		}
	}

	// "implements" should not be duplicated; only "new_keyword" added
	originalCount := len(BuiltinRelations[0].Synonyms)
	if len(implDef.Synonyms) != originalCount+1 {
		t.Errorf("expected %d synonyms (deduped), got %d: %v", originalCount+1, len(implDef.Synonyms), implDef.Synonyms)
	}
}

func TestValidRelationNames(t *testing.T) {
	defs := []RelationDef{
		{Name: "implements"},
		{Name: "regulates"},
	}
	names := ValidRelationNames(defs)
	if len(names) != 2 || names[0] != "implements" || names[1] != "regulates" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestRelationPatterns(t *testing.T) {
	defs := []RelationDef{
		{Name: "implements", Synonyms: []string{"implements"}},
		{Name: "cites", Synonyms: nil},          // should be skipped
		{Name: "derived_from", Synonyms: nil},    // should be skipped
		{Name: "regulates", Synonyms: []string{"regulates", "调控"}},
	}
	patterns := RelationPatterns(defs)

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns (skip empty synonyms), got %d", len(patterns))
	}
	if patterns[0].Relation != "implements" {
		t.Errorf("expected implements, got %q", patterns[0].Relation)
	}
	if patterns[1].Relation != "regulates" {
		t.Errorf("expected regulates, got %q", patterns[1].Relation)
	}
}

// Integration: zero config produces identical behavior to hardcoded builtins
func TestZeroConfigSameBehavior(t *testing.T) {
	merged := MergedRelations(nil)
	names := ValidRelationNames(merged)
	patterns := RelationPatterns(merged)

	// 8 built-in names
	if len(names) != 8 {
		t.Fatalf("expected 8 names, got %d", len(names))
	}

	// 6 patterns (cites + derived_from have empty synonyms)
	if len(patterns) != 6 {
		t.Fatalf("expected 6 patterns (skip empty synonyms), got %d", len(patterns))
	}

	// Store accepts all built-in types
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewStore(db, names, ValidEntityTypeNames(BuiltinEntityTypes))
	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})

	for _, name := range names {
		err := store.AddRelation(Relation{
			ID:       "r-" + name,
			SourceID: "a",
			TargetID: "b",
			Relation: name,
		})
		if err != nil {
			t.Errorf("built-in relation %q rejected: %v", name, err)
		}
	}
}

// Integration: custom relation type works end-to-end
func TestCustomRelationEndToEnd(t *testing.T) {
	cfg := []config.RelationConfig{
		{Name: "regulates", Synonyms: []string{"regulates", "regulated by"}},
	}
	merged := MergedRelations(cfg)
	names := ValidRelationNames(merged)
	patterns := RelationPatterns(merged)

	// 9 names (8 builtins + 1 custom)
	if len(names) != 9 {
		t.Fatalf("expected 9 names, got %d", len(names))
	}

	// 7 patterns (6 from builtins + 1 custom)
	if len(patterns) != 7 {
		t.Fatalf("expected 7 patterns, got %d", len(patterns))
	}

	// Store accepts the custom type
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewStore(db, names, ValidEntityTypeNames(BuiltinEntityTypes))
	store.AddEntity(Entity{ID: "gene-a", Type: TypeConcept, Name: "Gene A"})
	store.AddEntity(Entity{ID: "gene-b", Type: TypeConcept, Name: "Gene B"})

	err = store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "gene-a",
		TargetID: "gene-b",
		Relation: "regulates",
	})
	if err != nil {
		t.Fatalf("custom relation should be accepted: %v", err)
	}

	// Verify it was stored
	rels, err := store.GetRelations("gene-a", Outbound, "regulates")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 relation, got %d", len(rels))
	}
}

// Integration: extended built-in synonym triggers correct relation
func TestExtendedBuiltinSynonym(t *testing.T) {
	cfg := []config.RelationConfig{
		{Name: "implements", Synonyms: []string{"thực hiện"}},
	}
	merged := MergedRelations(cfg)
	patterns := RelationPatterns(merged)

	// Find the implements pattern
	var found bool
	for _, p := range patterns {
		if p.Relation == RelImplements {
			for _, kw := range p.Keywords {
				if kw == "thực hiện" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected Vietnamese synonym in implements pattern")
	}
}

// Integration: AddRelation with invalid type returns error (covers MCP path)
func TestAddRelationInvalidTypeError(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	names := ValidRelationNames(MergedRelations(nil))
	store := NewStore(db, names, ValidEntityTypeNames(BuiltinEntityTypes))
	store.AddEntity(Entity{ID: "a", Type: TypeConcept, Name: "A"})
	store.AddEntity(Entity{ID: "b", Type: TypeConcept, Name: "B"})

	err = store.AddRelation(Relation{
		ID:       "r1",
		SourceID: "a",
		TargetID: "b",
		Relation: "nonexistent_type",
	})
	if err == nil {
		t.Error("expected error for invalid relation type")
	}
	if err != nil && !strings.Contains(err.Error(), "unknown relation type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMergedRelationsDoesNotMutateBuiltins(t *testing.T) {
	originalLen := len(BuiltinRelations[0].Synonyms)
	cfg := []config.RelationConfig{
		{Name: "implements", Synonyms: []string{"foo", "bar"}},
	}
	MergedRelations(cfg)
	if len(BuiltinRelations[0].Synonyms) != originalLen {
		t.Error("MergedRelations mutated BuiltinRelations")
	}
}
