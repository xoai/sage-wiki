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

// Gates i1: accumulated merges must survive a later rename (M1), and two
// acronyms hitting one manifest concept must share one entry (M2).
func TestDedupRenameKeepsAccumulated(t *testing.T) {
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "rap", Sources: []string{"raw/1.md"}},
		{Name: "rap", Sources: []string{"raw/2.md"}}, // exact-name re-extract
		{Name: "remedial-action-plan", Aliases: []string{"rap"}, Sources: []string{"raw/3.md"}},
	}, nil)
	if len(out) != 1 || out[0].Name != "remedial-action-plan" {
		t.Fatalf("want 1 canonical, got %+v", out)
	}
	if len(out[0].Sources) != 3 {
		t.Errorf("accumulated sources lost on rename: %v", out[0].Sources)
	}
}

func TestDedupTwoAcronymsOneCanonical(t *testing.T) {
	existing := map[string]manifest.Concept{
		"remedial-action-plan": {ArticlePath: "wiki/x.md", Sources: []string{"raw/old.md"}, Aliases: []string{"rap", "r.a.p"}},
	}
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "rap", Sources: []string{"raw/1.md"}},
		{Name: "r.a.p", Sources: []string{"raw/2.md"}},
	}, existing)
	if len(out) != 1 || out[0].Name != "remedial-action-plan" {
		t.Fatalf("want exactly one canonical entry, got %+v", out)
	}
	if len(out[0].Sources) != 3 {
		t.Errorf("sources = %v, want old+1+2 deduped", out[0].Sources)
	}
}

// Transitive alias merge: an alias merged mid-loop must be visible to later
// concepts.
func TestDedupTransitiveAlias(t *testing.T) {
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "remedial-action-plan", Aliases: []string{"rap"}, Sources: []string{"raw/1.md"}},
		{Name: "rap", Aliases: []string{"wsp"}, Sources: []string{"raw/2.md"}}, // folds, gains wsp as alias
		{Name: "wsp", Sources: []string{"raw/3.md"}},                           // must see the merged alias
	}, nil)
	if len(out) != 1 {
		t.Fatalf("transitive fold: got %+v", out)
	}
	if len(out[0].Sources) != 3 {
		t.Errorf("sources = %v, want all three", out[0].Sources)
	}
}

// Review M1: rule2 → rule1-rename → rule2 must not merge into a detached
// entry. The stale seen key would have swallowed the third acronym's data.
func TestDedupRule2RenameRule2Chain(t *testing.T) {
	existing := map[string]manifest.Concept{
		"remedial-action-plan": {ArticlePath: "wiki/x.md", Sources: []string{"raw/old.md"}, Aliases: []string{"rap"}},
	}
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "rap", Sources: []string{"raw/1.md"}},                                                                 // rule 2 entry
		{Name: "remedial action planning", Aliases: []string{"remedial-action-plan"}, Sources: []string{"raw/2.md"}}, // rule 1: its alias matches the canonical name
		{Name: "rap", Sources: []string{"raw/3.md"}},                                                                 // rule 2 again — must reach the WINNER
	}, existing)
	if len(out) != 1 {
		t.Fatalf("want 1 entry, got %+v", out)
	}
	total := 0
	for _, c := range out {
		total += len(c.Sources)
	}
	if total < 4 {
		t.Errorf("evidence lost to a detached entry: total sources = %d, want >= 4 (old+1+2+3), %+v", total, out)
	}
}

// Review: self-alias must never land in Aliases (A→B→C→A chain).
func TestDedupNoSelfAlias(t *testing.T) {
	out := deduplicateConcepts([]ExtractedConcept{
		{Name: "alpha-long", Aliases: []string{"beta-long"}, Sources: []string{"raw/1.md"}},
		{Name: "beta-long", Aliases: []string{"alpha-long"}, Sources: []string{"raw/2.md"}},
	}, nil)
	for _, c := range out {
		for _, a := range c.Aliases {
			if a == c.Name {
				t.Errorf("self-alias %q on %q", a, c.Name)
			}
		}
	}
}

// Review: normalized alias dedup — "RAP" and "rap" do not accumulate.
func TestDedupNormalizedAliasSet(t *testing.T) {
	existing := map[string]manifest.Concept{
		"remedial-action-plan": {ArticlePath: "wiki/x.md", Sources: []string{"raw/old.md"}, Aliases: []string{"RAP"}},
	}
	out := deduplicateConcepts([]ExtractedConcept{{Name: "rap", Sources: []string{"raw/1.md"}}}, existing)
	count := 0
	for _, a := range out[0].Aliases {
		if a == "RAP" || a == "rap" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("normalized dedup: aliases = %v", out[0].Aliases)
	}
}
