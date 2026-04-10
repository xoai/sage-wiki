package ontology

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
)

func TestMergedEntityTypesEmptyConfig(t *testing.T) {
	merged := MergedEntityTypes(nil)
	if len(merged) != len(BuiltinEntityTypes) {
		t.Fatalf("expected %d builtins, got %d", len(BuiltinEntityTypes), len(merged))
	}
	for i, b := range BuiltinEntityTypes {
		if merged[i].Name != b.Name {
			t.Errorf("index %d: expected %q, got %q", i, b.Name, merged[i].Name)
		}
	}
}

func TestMergedEntityTypesExtendBuiltin(t *testing.T) {
	cfg := []config.EntityTypeConfig{
		{Name: "concept", Description: "Custom description for concept"},
	}
	merged := MergedEntityTypes(cfg)

	// Should still be 5 total (no new types)
	if len(merged) != len(BuiltinEntityTypes) {
		t.Fatalf("expected %d, got %d", len(BuiltinEntityTypes), len(merged))
	}

	// Description should be overridden
	if merged[0].Description != "Custom description for concept" {
		t.Errorf("expected custom description, got %q", merged[0].Description)
	}
}

func TestMergedEntityTypesAddCustom(t *testing.T) {
	cfg := []config.EntityTypeConfig{
		{Name: "conversation", Description: "A dialogue or discussion"},
	}
	merged := MergedEntityTypes(cfg)

	if len(merged) != len(BuiltinEntityTypes)+1 {
		t.Fatalf("expected %d, got %d", len(BuiltinEntityTypes)+1, len(merged))
	}

	last := merged[len(merged)-1]
	if last.Name != "conversation" {
		t.Errorf("expected conversation, got %q", last.Name)
	}
	if last.Description != "A dialogue or discussion" {
		t.Errorf("expected description, got %q", last.Description)
	}
}

func TestMergedEntityTypesDoesNotMutateBuiltins(t *testing.T) {
	originalDesc := BuiltinEntityTypes[0].Description
	cfg := []config.EntityTypeConfig{
		{Name: "concept", Description: "Overridden"},
	}
	MergedEntityTypes(cfg)
	if BuiltinEntityTypes[0].Description != originalDesc {
		t.Error("MergedEntityTypes mutated BuiltinEntityTypes")
	}
}

func TestValidEntityTypeNames(t *testing.T) {
	defs := []EntityTypeDef{
		{Name: "concept"},
		{Name: "conversation"},
	}
	names := ValidEntityTypeNames(defs)
	if len(names) != 2 || names[0] != "concept" || names[1] != "conversation" {
		t.Errorf("unexpected names: %v", names)
	}
}

// Integration: zero config produces identical behavior to hardcoded builtins
func TestZeroEntityConfigSameBehavior(t *testing.T) {
	merged := MergedEntityTypes(nil)
	names := ValidEntityTypeNames(merged)

	// 5 built-in names
	if len(names) != 5 {
		t.Fatalf("expected 5 names, got %d", len(names))
	}

	// Store accepts all built-in types
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewStore(db, ValidRelationNames(BuiltinRelations), names)
	for _, name := range names {
		err := store.AddEntity(Entity{
			ID:   "e-" + name,
			Type: name,
			Name: "Entity " + name,
		})
		if err != nil {
			t.Errorf("built-in entity type %q rejected: %v", name, err)
		}
	}
}

// Integration: custom entity type works end-to-end
func TestCustomEntityTypeEndToEnd(t *testing.T) {
	cfg := []config.EntityTypeConfig{
		{Name: "conversation", Description: "A dialogue"},
	}
	merged := MergedEntityTypes(cfg)
	names := ValidEntityTypeNames(merged)

	// 6 names (5 builtins + 1 custom)
	if len(names) != 6 {
		t.Fatalf("expected 6 names, got %d", len(names))
	}

	// Store accepts the custom type
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewStore(db, ValidRelationNames(BuiltinRelations), names)
	err = store.AddEntity(Entity{
		ID:   "conv-1",
		Type: "conversation",
		Name: "Design Discussion",
	})
	if err != nil {
		t.Fatalf("custom entity type should be accepted: %v", err)
	}

	// Verify it was stored
	entity, err := store.GetEntity("conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if entity == nil {
		t.Fatal("expected entity, got nil")
	}
	if entity.Type != "conversation" {
		t.Errorf("expected type conversation, got %q", entity.Type)
	}
}

// Integration: AddEntity with invalid type returns error
func TestAddEntityInvalidTypeError(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	names := ValidEntityTypeNames(MergedEntityTypes(nil))
	store := NewStore(db, ValidRelationNames(BuiltinRelations), names)

	err = store.AddEntity(Entity{
		ID:   "e1",
		Type: "nonexistent_type",
		Name: "Test",
	})
	if err == nil {
		t.Error("expected error for invalid entity type")
	}
	if err != nil && !strings.Contains(err.Error(), "unknown entity type") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Integration: nil validEntityTypes accepts all types (permissive mode)
func TestNilValidEntityTypesAcceptsAll(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewStore(db, nil, nil)
	err = store.AddEntity(Entity{
		ID:   "e1",
		Type: "anything_goes",
		Name: "Test",
	})
	if err != nil {
		t.Errorf("nil validEntityTypes should accept all types: %v", err)
	}
}
