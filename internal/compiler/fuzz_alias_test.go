package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
)

// FuzzAliasNormalize (SPEC-08 D7 target 3, P2-5 conventions): normalizeName,
// normalizeNameTokens, ontology.FormatConceptName, and buildAliasMap over
// arbitrary names and aliases. Assertions are SECURITY INVARIANTS ONLY — no
// panic, deterministic output, and the canonical-id self-mapping property of
// buildAliasMap. Errors are accepted, never asserted.
func FuzzAliasNormalize(f *testing.F) {
	for _, seed := range []string{
		"Kubernetes",
		"k8s",
		"api-gateway",
		"  padded  name  ",
		"UPPER-case-MIXED",
		"unicode-cjk-概念",
		"",
	} {
		f.Add(seed, "alias-one", "alias-two")
	}
	f.Fuzz(func(t *testing.T, name, aliasA, aliasB string) {
		// Pure normalizers: no panic, deterministic.
		if normalizeName(name) != normalizeName(name) {
			t.Fatalf("non-deterministic normalizeName")
		}
		toksA := normalizeNameTokens(name)
		toksB := normalizeNameTokens(name)
		if len(toksA) != len(toksB) {
			t.Fatalf("non-deterministic normalizeNameTokens length")
		}
		for i := range toksA {
			if toksA[i] != toksB[i] {
				t.Fatalf("non-deterministic normalizeNameTokens content")
			}
		}
		if ontology.FormatConceptName(name) != ontology.FormatConceptName(name) {
			t.Fatalf("non-deterministic FormatConceptName")
		}

		// buildAliasMap over a single derived concept: no panic,
		// deterministic, and the concept's own canonical id maps to itself.
		// (One concept keeps the self-mapping invariant collision-free; with
		// several concepts a later FormatConceptName can legitimately shadow
		// an earlier canonical key, which is correct behavior, not a bug.)
		concepts := []ExtractedConcept{
			{Name: name, Aliases: []string{aliasA, aliasB}},
		}
		first := buildAliasMap(concepts, nil)
		second := buildAliasMap(concepts, nil)
		if len(first) != len(second) {
			t.Fatalf("non-deterministic buildAliasMap size")
		}
		for k, v := range first {
			if second[k] != v {
				t.Fatalf("non-deterministic buildAliasMap for key %q", k)
			}
		}
		if name != "" {
			if got := first[name]; got != name {
				t.Fatalf("canonical id %q must map to itself, got %q", name, got)
			}
		}
	})
}
