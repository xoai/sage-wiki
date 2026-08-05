package web

import (
	"testing"
)

// FuzzFrontmatter (SPEC-08 D7 target 1, web site, P2-5 conventions): feeds
// parseFrontmatterSimple, the web-owned pure-string frontmatter parser.
// Assertions are SECURITY INVARIANTS ONLY — no panic, deterministic parse,
// bounded output. Errors are accepted, never asserted. The extract/wiki/
// ontology/compiler frontmatter sites carry same-named targets in their own
// packages (Go visibility forbids one cross-package target).
func FuzzFrontmatter(f *testing.F) {
	for _, seed := range []string{
		"title: hello\nentity_type: concept",
		"key: value with: colon\nother: x",
		"",
		":\n::\nkey:",
		"no colon line",
		"  spaced  :  value  ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, fm string) {
		first := parseFrontmatterSimple(fm)
		second := parseFrontmatterSimple(fm)
		if len(first) != len(second) {
			t.Fatalf("non-deterministic parse: %d vs %d keys", len(first), len(second))
		}
		for k, v := range first {
			if sv, ok := second[k]; !ok || sv != v {
				t.Fatalf("non-deterministic parse for key %q", k)
			}
		}
		// Bounded output: at most one entry per input line.
		if int64(len(first)) > int64(len(fm))+1 {
			t.Fatalf("parse produced more keys than input lines")
		}
	})
}
