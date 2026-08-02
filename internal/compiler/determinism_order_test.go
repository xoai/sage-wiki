package compiler

import (
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// TestSortedConceptNames_Deterministic pins SPEC-04 D1: the extract-prompt
// dedup snapshot must not depend on Go map iteration order.
func TestSortedConceptNames_Deterministic(t *testing.T) {
	m := map[string]manifest.Concept{}
	for _, n := range []string{"zebra", "alpha", "middleware", "beta", "gamma"} {
		m[n] = manifest.Concept{}
	}
	want := strings.Join([]string{"alpha", "beta", "gamma", "middleware", "zebra"}, ", ")
	for i := 0; i < 20; i++ {
		got := strings.Join(sortedConceptNames(m), ", ")
		if got != want {
			t.Fatalf("iteration %d: got %q, want %q", i, got, want)
		}
	}
}

// TestManifestConceptRefs_Sorted pins the defensive sort on the
// write-pass concept refs (SPEC-04 D1).
func TestManifestConceptRefs_Sorted(t *testing.T) {
	m := map[string]manifest.Concept{}
	for _, n := range []string{"zebra", "alpha", "middleware"} {
		m[n] = manifest.Concept{}
	}
	for i := 0; i < 20; i++ {
		refs := manifestConceptRefs(m)
		if len(refs) != 3 {
			t.Fatalf("got %d refs, want 3", len(refs))
		}
		if refs[0].Name != "alpha" || refs[1].Name != "middleware" || refs[2].Name != "zebra" {
			t.Fatalf("iteration %d: unsorted refs: %v, %v, %v", i, refs[0].Name, refs[1].Name, refs[2].Name)
		}
	}
}

// TestDedupCache_TieBreakDeterministic pins SPEC-04 D1: when two existing
// concepts have EQUAL cosine similarity to a probe, the merge target is the
// canonically-first name — not the Go map iteration order.
func TestDedupCache_TieBreakDeterministic(t *testing.T) {
	probe := []float32{1, 0, 0, 0}
	shared := []float32{0.9, 0.1, 0.0, 0.1}
	embedder := &mockEmbedder{
		embeddings: map[string][]float32{
			"probe-concept": probe,
			"beta-concept":  shared,
			"alpha-concept": shared,
		},
	}

	// Build the cache with different seed orders; the winner must not vary.
	for _, seedOrder := range [][]string{
		{"beta-concept", "alpha-concept"},
		{"alpha-concept", "beta-concept"},
	} {
		dc := NewDedupCache(embedder, nil, 0.85)
		dc.Seed(seedOrder)
		match, _, _ := dc.CheckDuplicate("probe-concept")
		if match != "alpha-concept" {
			t.Fatalf("seed %v: tie broke to %q, want canonical-first %q", seedOrder, match, "alpha-concept")
		}
	}
}

// TestBuildFrontmatter_SortsAliasesAndSources pins the canonical emission
// (SPEC-04 D1 / plan-review F-035): shuffled input slices → identical bytes.
func TestBuildFrontmatter_SortsAliasesAndSources(t *testing.T) {
	c1 := ExtractedConcept{
		Name:    "alpha",
		Aliases: []string{"zebra-alias", "beta-alias", "alpha-alias"},
		Sources: []string{"raw/c.md", "raw/a.md", "raw/b.md"},
	}
	c2 := ExtractedConcept{
		Name:    "alpha",
		Aliases: []string{"alpha-alias", "beta-alias", "zebra-alias"},
		Sources: []string{"raw/a.md", "raw/b.md", "raw/c.md"},
	}
	fm1 := buildFrontmatter(c1, "concept", map[string]string{}, nil, time.UTC)
	fm2 := buildFrontmatter(c2, "concept", map[string]string{}, nil, time.UTC)
	if fm1 != fm2 {
		t.Fatalf("shuffled slices produced different frontmatter:\n--- c1 ---\n%s\n--- c2 ---\n%s", fm1, fm2)
	}
	if !strings.Contains(fm1, `aliases: ["alpha-alias", "beta-alias", "zebra-alias"]`) {
		t.Errorf("aliases not sorted ascending:\n%s", fm1)
	}
	if !strings.Contains(fm1, `sources: ["raw/a.md", "raw/b.md", "raw/c.md"]`) {
		t.Errorf("sources not sorted ascending:\n%s", fm1)
	}
}
