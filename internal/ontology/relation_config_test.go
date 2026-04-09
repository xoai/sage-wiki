package ontology

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestDefaultRelationTypeDefs(t *testing.T) {
	defs := DefaultRelationTypeDefs()
	if len(defs) != 8 {
		t.Errorf("expected 8 default relation types, got %d", len(defs))
	}
	if defs[0].Name != "implements" {
		t.Errorf("expected first type 'implements', got %q", defs[0].Name)
	}
}

func TestValidRelation(t *testing.T) {
	defs := []config.RelationTypeDef{
		{Name: "implements", Synonyms: []string{"实现了", "implementation of"}},
		{Name: "amends", Synonyms: []string{"修订", "废止"}},
	}

	tests := []struct {
		relation string
		valid    bool
	}{
		{"implements", true},
		{"amends", true},
		{"实现了", true},
		{"修订", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		got := ValidRelation(tt.relation, defs)
		if got != tt.valid {
			t.Errorf("ValidRelation(%q) = %v, want %v", tt.relation, got, tt.valid)
		}
	}
}

func TestNormalizeRelation(t *testing.T) {
	defs := []config.RelationTypeDef{
		{Name: "implements", Synonyms: []string{"实现了", "implementation of"}},
		{Name: "amends", Synonyms: []string{"修订", "废止"}},
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"implements", "implements"},
		{"实现了", "implements"},
		{"修订", "amends"},
		{"废止", "amends"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := NormalizeRelation(tt.input, defs)
		if got != tt.expected {
			t.Errorf("NormalizeRelation(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
