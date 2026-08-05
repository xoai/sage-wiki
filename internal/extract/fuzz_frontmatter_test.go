package extract

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzFrontmatter (SPEC-08 D7 target 1, P2-5 conventions) drives the
// extract-owned frontmatter site: extractMarkdown's `---\n…\n---` split.
// Assertions are SECURITY INVARIANTS ONLY — no panic, no unbounded growth,
// deterministic split. Errors are accepted, never asserted.
//
// The other pure-string frontmatter sites live in their owning packages
// (web parseFrontmatterSimple, ontology frontmatterEntityType, wiki
// stripFrontmatter, compiler stripLLMFrontmatter) and each carries a
// same-named FuzzFrontmatter target there — Go visibility forbids one
// cross-package target (cycle decision, Task 17).
func FuzzFrontmatter(f *testing.F) {
	for _, seed := range seedsFrontmatter() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFuzzInput(t, data, ".md")

		first, err := extractMarkdown(path, "auto")
		if err != nil {
			return // errors are not invariant violations
		}

		// Growth invariant: the split only partitions input bytes (TrimSpace
		// can only shrink), so frontmatter+text never exceed the input.
		if int64(len(first.Frontmatter)+len(first.Text)) > int64(len(data)) {
			t.Fatalf("growth invariant violated: fm %d + text %d > input %d",
				len(first.Frontmatter), len(first.Text), len(data))
		}

		// Determinism invariant: a second split of the same bytes is equal.
		second, err := extractMarkdown(path, "auto")
		if err != nil {
			return
		}
		if first.Frontmatter != second.Frontmatter || first.Text != second.Text {
			t.Fatalf("non-deterministic frontmatter split")
		}
	})
}

// seedsFrontmatter returns programmatic frontmatter shapes that reach the
// split's success path, plus the golden-corpus adversarial/unicode markdown
// files (ready-made hostile inputs, spec D7).
func seedsFrontmatter() [][]byte {
	seeds := [][]byte{
		[]byte("---\ntitle: hello\nentity_type: concept\n---\nbody text\n"),
		[]byte("---\n---\n"),
		[]byte("---\nkey: value with: colon\n---\n---\nnested separator\n"),
		[]byte("no frontmatter at all"),
		[]byte("---\nunclosed frontmatter\n"),
		[]byte("---\r\ncrlf frontmatter\r\n---\r\ncrlf body\r\n"),
	}
	base := filepath.Join("..", "..", "testdata", "golden-corpus")
	for _, sub := range []string{"adversarial", "unicode"} {
		entries, err := os.ReadDir(filepath.Join(base, sub))
		if err != nil {
			continue // corpus optional at fuzz-seed time
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(base, sub, e.Name())); err == nil {
				seeds = append(seeds, data)
			}
		}
	}
	return seeds
}
