package compiler

import (
	"testing"
)

// FuzzFrontmatter (SPEC-08 D7 target 1, compiler site, P2-5 conventions):
// feeds stripLLMFrontmatter, the compiler-owned stripper for LLM-emitted
// frontmatter (code-fenced and bare). Assertions are SECURITY INVARIANTS
// ONLY — no panic, deterministic result, output never larger than input.
// Errors are accepted, never asserted.
func FuzzFrontmatter(f *testing.F) {
	for _, seed := range []string{
		"```yaml\n---\ntitle: x\n---\n```\nbody",
		"---\ntitle: x\n---\nbody",
		"```\n---\na: b\n---\n```",
		"---\nunclosed",
		"plain article, no frontmatter",
		"```markdown\nwrapped whole response\n```",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		first := stripLLMFrontmatter(content)
		second := stripLLMFrontmatter(content)
		if first != second {
			t.Fatalf("non-deterministic LLM frontmatter strip")
		}
		if int64(len(first)) > int64(len(content)) {
			t.Fatalf("strip grew the input: %d > %d", len(first), len(content))
		}
	})
}
