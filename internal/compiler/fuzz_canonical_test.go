package compiler

import (
	"strings"
	"testing"
	"time"
)

// FuzzCanonical (SPEC-08 D7 target 4, P2-5 conventions): buildFrontmatter +
// canonicalSubsetJSON over arbitrary inputs. Invariants: no panic, and
// determinism — f(x) == f(x) on double invocation. SOURCE_DATE_EPOCH pins
// the wall clock so buildFrontmatter's created_at is part of the comparison
// (the project's own reproducibility hook). Errors are accepted, never
// asserted.
func FuzzCanonical(f *testing.F) {
	for _, seed := range [][3]string{
		{"kubernetes", "concept", "k8s"},
		{"api-gateway", "technique", "gateway"},
		{"", "concept", ""},
		{"unicode-概念", "claim", "别名"},
	} {
		f.Add(seed[0], seed[1], seed[2], "custom_field", "custom value")
	}
	f.Fuzz(func(t *testing.T, name, entityType, alias, fieldK, fieldV string) {
		t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

		concept := ExtractedConcept{
			Name:    name,
			Aliases: []string{alias, alias}, // duplicates exercise sortedCopy's set semantics
			Sources: []string{"raw/doc.md"},
		}
		fields := map[string]string{"confidence": "high"}
		var order []string
		if fieldK != "" && fieldK != "confidence" {
			fields[fieldK] = fieldV
			order = []string{fieldK}
		}

		first := buildFrontmatter(concept, entityType, fields, order, time.UTC)
		second := buildFrontmatter(concept, entityType, fields, order, time.UTC)
		if first != second {
			t.Fatalf("non-deterministic buildFrontmatter:\n%s\n---\n%s", first, second)
		}
		if !strings.HasPrefix(first, "---\n") || !strings.HasSuffix(first, "---") {
			t.Fatalf("buildFrontmatter lost its YAML document frame")
		}

		subset := map[string]any{
			"name":    name,
			"aliases": []string{alias},
			"nested":  map[string]any{fieldK: fieldV, "z": "last", "a": "first"},
		}
		j1, err1 := canonicalSubsetJSON(subset)
		j2, err2 := canonicalSubsetJSON(subset)
		if err1 != nil || err2 != nil {
			return // marshal errors are accepted, never asserted
		}
		if j1 != j2 {
			t.Fatalf("non-deterministic canonicalSubsetJSON:\n%s\n---\n%s", j1, j2)
		}
	})
}
