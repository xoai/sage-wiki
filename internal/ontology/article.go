package ontology

import (
	"strings"
	"unicode"

	"github.com/xoai/sage-wiki/internal/store"
)

// FormatConceptName turns a concept id into its display name:
// "self-attention" → "Self Attention".
//
// It lives here rather than in internal/compiler because three packages that
// index an already-written article need it — internal/wiki, internal/mcp and
// cmd/sage-wiki — and internal/compiler cannot supply it: eight of that
// package's own test files import internal/wiki, so an internal/wiki →
// internal/compiler edge closes an import cycle. The cycle is invisible to
// `go build` and only surfaces under `go vet` / `go test`.
func FormatConceptName(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

// ArticleEntityType reads a compiled article's declared entity type from its
// YAML frontmatter, falling back to TypeConcept.
//
// The key is `entity_type`, not `type` — compiled articles are written with
// "---\nconcept: …\nentity_type: …\n" and carry no `type` key at all, so a
// `type:` parse would silently never match and every article would re-index as
// a concept. That is the exact downgrade this function exists to prevent: now
// that AddEntity writes `type` unconditionally, a caller that hard-codes
// TypeConcept actively demotes a `technique` on every reindex.
//
// The parsed value is validated against the store's configured types with the
// same fallback WriteArticles uses. Without that, an article whose type came
// from a since-uninstalled pack would make AddEntity return an error — and
// reconcile returns that error rather than warning, so the article would fail
// to reindex on every run, forever.
func ArticleEntityType(articleContent string, ont store.OntologyStore) string {
	t := frontmatterEntityType(articleContent)
	if t == "" || ont == nil || !ont.IsValidType(t) {
		return TypeConcept
	}
	return t
}

// frontmatterEntityType extracts `entity_type` from a leading `---` block.
// Returns "" when there is no frontmatter or no such key.
func frontmatterEntityType(content string) string {
	// Normalize line endings first. Without this, an article saved by a CRLF
	// editor misses the prefix match, falls back to TypeConcept, and — because
	// AddEntity now writes `type` unconditionally — is demoted from its
	// declared type on EVERY reconcile. That is the failure this function
	// exists to prevent, reached by another route.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return ""
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "entity_type:")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
