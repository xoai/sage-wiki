package compiler

import (
	"testing"
)

// FuzzWikilink (SPEC-08 D7 target 2, P2-5 conventions): wikilinkRe matching,
// sanitizeWikilinks with a derived alias map, and the StripBrokenWikilinks
// replacement core over arbitrary content. Assertions are SECURITY
// INVARIANTS ONLY — no panic, deterministic output, bounded growth. Errors
// are accepted, never asserted.
func FuzzWikilink(f *testing.F) {
	for _, seed := range []string{
		"link [[alpha]] here",
		"piped [[alpha|Alpha]] link",
		"adjacent [[a]][[b]] links",
		"empty [[]] link",
		"nested [[outer [[inner]] ]] brackets",
		"unclosed [[dangling",
		"no links at all",
		"[[|display-only]]",
	} {
		f.Add(seed, "alpha", "canonical-alpha")
	}
	f.Fuzz(func(t *testing.T, content, target, mapped string) {
		aliasMap := map[string]string{target: mapped}

		first := sanitizeWikilinks(content, aliasMap)
		second := sanitizeWikilinks(content, aliasMap)
		if first != second {
			t.Fatalf("non-deterministic sanitizeWikilinks")
		}

		// Empty alias map is the documented no-op path.
		if got := sanitizeWikilinks(content, nil); got != content {
			t.Fatalf("empty alias map must be a no-op")
		}

		// StripBrokenWikilinks core: a link whose target is not in the
		// existing set is replaced by its raw target text. Reproduced here
		// (the production copy is filesystem-bound) with the same regex.
		existing := map[string]bool{mapped: true}
		stripped := wikilinkRe.ReplaceAllStringFunc(content, func(match string) string {
			inner := match[2 : len(match)-2]
			if existing[inner] {
				return match
			}
			return inner
		})
		again := wikilinkRe.ReplaceAllStringFunc(content, func(match string) string {
			inner := match[2 : len(match)-2]
			if existing[inner] {
				return match
			}
			return inner
		})
		if stripped != again {
			t.Fatalf("non-deterministic strip core")
		}
		// The strip core never invents bytes: every output rune comes from
		// the input (matches are replaced by their own inner text).
		if len(stripped) > len(content) {
			t.Fatalf("strip core grew the input: %d > %d", len(stripped), len(content))
		}
	})
}
