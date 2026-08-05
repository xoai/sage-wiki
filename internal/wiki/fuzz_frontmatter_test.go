package wiki

import (
	"testing"
)

// FuzzFrontmatter (SPEC-08 D7 target 1, wiki site, P2-5 conventions): feeds
// stripFrontmatter, the wiki-owned pure-string frontmatter stripper.
// Assertions are SECURITY INVARIANTS ONLY — no panic, deterministic result,
// output never larger than input. Errors are accepted, never asserted.
func FuzzFrontmatter(f *testing.F) {
	for _, seed := range []string{
		"---\ntitle: hello\n---\nbody text",
		"---\ntitle: x\n---\n\nleading blank preserved",
		"---\nunclosed frontmatter",
		"no frontmatter",
		"---\n---\n",
		"---\na: b\n---\n---\nnested separator",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		first := stripFrontmatter(s)
		second := stripFrontmatter(s)
		if first != second {
			t.Fatalf("non-deterministic strip")
		}
		if int64(len(first)) > int64(len(s)) {
			t.Fatalf("strip grew the input: %d > %d", len(first), len(s))
		}
	})
}
