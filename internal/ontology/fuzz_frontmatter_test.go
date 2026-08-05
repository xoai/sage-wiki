package ontology

import (
	"testing"
)

// FuzzFrontmatter (SPEC-08 D7 target 1, ontology site, P2-5 conventions):
// feeds frontmatterEntityType, the ontology-owned pure-string reader of the
// entity_type field. Assertions are SECURITY INVARIANTS ONLY — no panic,
// deterministic result. Errors are accepted, never asserted.
func FuzzFrontmatter(f *testing.F) {
	for _, seed := range []string{
		"---\nentity_type: concept\n---\nbody",
		"---\r\nentity_type: technique\r\n---\r\ncrlf body",
		"---\nother: x\nentity_type: \"claim\"\n---\n",
		"---\nentity_type:\n---\n",
		"no frontmatter",
		"---\nunclosed",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		first := frontmatterEntityType(content)
		second := frontmatterEntityType(content)
		if first != second {
			t.Fatalf("non-deterministic entity_type: %q vs %q", first, second)
		}
	})
}
