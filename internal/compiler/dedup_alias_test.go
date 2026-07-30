package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// Issue #128 T2: alias-overlap dedup — exact-normalized matches only.

func TestDedupWithinSetAliasBothDirections(t *testing.T) {
	// New name matches an existing concept's alias.
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "remedial-action-plan", Aliases: []string{"rap"}, Sources: []string{"raw/a.md"}},
		{Name: "rap", Sources: []string{"raw/b.md"}},
	}, nil)
	if len(out) != 1 {
		t.Fatalf("want 1 merged concept, got %d: %+v", len(out), out)
	}
	if out[0].Name != "remedial-action-plan" {
		t.Errorf("canonical = %q, want remedial-action-plan", out[0].Name)
	}
	if len(out[0].Sources) != 2 {
		t.Errorf("sources = %v, want both", out[0].Sources)
	}

	// New concept's alias matches an existing name.
	out = deduplicateConcepts([]ExtractedConcept{
		{Name: "rap", Aliases: []string{"remedial-action-plan"}, Sources: []string{"raw/a.md"}},
		{Name: "remedial-action-plan", Sources: []string{"raw/b.md"}},
	}, nil)
	if len(out) != 1 || out[0].Name != "remedial-action-plan" {
		t.Errorf("alias→name direction: %+v", out)
	}
}

func TestDedupAgainstManifestAliases(t *testing.T) {
	existing := map[string]manifest.Concept{
		"remedial-action-plan": {ArticlePath: "wiki/concepts/remedial-action-plan.md", Sources: []string{"raw/old.md"}, Aliases: []string{"rap"}},
	}
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "RAP ", Sources: []string{"raw/new.md"}}, // case + whitespace normalize
	}, existing)
	if len(out) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(out), out)
	}
	if out[0].Name != "remedial-action-plan" {
		t.Errorf("renamed to canonical %q, want remedial-action-plan", out[0].Name)
	}
	if len(out[0].Sources) != 2 {
		t.Errorf("unioned sources = %v, want old+new", out[0].Sources)
	}
	// A's name joins the alias list for future merges.
	found := false
	for _, a := range out[0].Aliases {
		if a == "rap" {
			found = true
		}
	}
	if !found {
		t.Errorf("aliases = %v, want to include rap", out[0].Aliases)
	}
}

func TestDedupNoMatchPassThrough(t *testing.T) {
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "wsp", Sources: []string{"raw/a.md"}},
		{Name: "rdip", Sources: []string{"raw/b.md"}},
	}, nil)
	if len(out) != 2 {
		t.Errorf("no-match must keep both, got %+v", out)
	}
}

func TestDedupEmptyManifestMap(t *testing.T) {
	out := deduplicateConcepts([]ExtractedConcept{{Name: "a", Sources: []string{"raw/a.md"}}}, map[string]manifest.Concept{})
	if len(out) != 1 || out[0].Name != "a" {
		t.Errorf("empty manifest map must be inert: %+v", out)
	}
}
