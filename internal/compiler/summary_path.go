package compiler

import (
	"path/filepath"
	"strings"
)

// SummaryFilename converts a source path to a unique summary filename.
//
// Using only filepath.Base() causes collisions when multiple sources share
// the same basename — e.g. docs/projects/claw/manifest.md and
// docs/projects/workflow/manifest.md both become manifest.md, and later
// compilations silently overwrite earlier ones (issue #51).
//
// The algorithm preserves every meaningful path segment by joining them with
// hyphens, then sanitizes any remaining dots so the produced filename has a
// single trailing ".md":
//
//	"raw/paper.md"                                 → "raw-paper.md"
//	"docs/projects/claw/manifest.md"               → "docs-projects-claw-manifest.md"
//	"docs/projects/workflow/manifest.md"           → "docs-projects-workflow-manifest.md"
//	"../../ezra/docs/projects/claw/manifest.md"    → "ezra-docs-projects-claw-manifest.md"
//	"raw/data.txt"                                 → "raw-data-txt.md"
//	"raw/data.md"                                  → "raw-data.md" (no collision with the above)
//	"manifest.md"                                  → "manifest.md"
//
// Two paths produce the same filename if and only if they normalize to the
// same sequence of non-dot, non-".." segments — which is exactly the
// equivalence we want (the diff walker already dedupes paths that resolve to
// the same file on disk).
func SummaryFilename(sourcePath string) string {
	p := filepath.ToSlash(filepath.Clean(sourcePath))
	parts := strings.Split(p, "/")

	// Drop "", ".", ".." segments so paths that traverse out of the project
	// (../../...) produce sensible filenames built from the first concrete
	// path segment onward.
	cleaned := parts[:0]
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			continue
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "summary.md"
	}

	// Drop a trailing .md (case-insensitive) from the LAST segment only so
	// the join produces "*-name.md" rather than "*-name.md.md".
	last := cleaned[len(cleaned)-1]
	if strings.EqualFold(filepath.Ext(last), ".md") {
		cleaned[len(cleaned)-1] = strings.TrimSuffix(last, filepath.Ext(last))
	}

	joined := strings.Join(cleaned, "-")

	// Replace any remaining "." (from non-.md extensions or unusual segment
	// names) with "-" so the result has exactly one trailing ".md". Without
	// this, "raw/data.txt" → "raw-data.txt.md" works on disk but reads
	// inconsistently; collapsing dots avoids that ambiguity.
	joined = strings.ReplaceAll(joined, ".", "-")

	return joined + ".md"
}

// SummaryFilenameMode converts a source path to a summary filename under the
// configured naming scheme (issue #107).
//
// mode "full" (the default) delegates to SummaryFilename — byte-identical to
// historical behavior. mode "relative" strips the configured sourceRoot prefix
// and collapses a duplicated trailing path segment, producing cleaner names for
// nested source trees:
//
//	full:      "wiki/pdf2md/Final FS Letter/Final FS Letter.md"
//	             → "wiki-pdf2md-Final FS Letter-Final FS Letter.md"
//	relative:  same path, root "wiki/pdf2md"
//	             → "Final FS Letter.md"
//
// relative mode trades some cross-path collision safety for readability: two
// source roots sharing a relative subpath, or a top-level "foo.md" alongside
// "foo/foo.md", can collide. "full" remains the collision-safe default.
func SummaryFilenameMode(sourcePath, sourceRoot, mode string) string {
	if mode != "relative" {
		return SummaryFilename(sourcePath)
	}

	p := filepath.ToSlash(filepath.Clean(sourcePath))
	parts := strings.Split(p, "/")

	// Strip the source-root prefix so the summary name is relative to the
	// source the file was discovered under.
	if sourceRoot != "" {
		rootParts := strings.Split(filepath.ToSlash(filepath.Clean(sourceRoot)), "/")
		if hasSegmentPrefix(parts, rootParts) {
			parts = parts[len(rootParts):]
		}
	}

	cleaned := parts[:0]
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			continue
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "summary.md"
	}

	// Trim a trailing .md from the LAST segment BEFORE the duplicate compare —
	// the raw last segment "Final FS Letter.md" only equals the parent dir
	// "Final FS Letter" once the extension is removed.
	last := cleaned[len(cleaned)-1]
	if strings.EqualFold(filepath.Ext(last), ".md") {
		cleaned[len(cleaned)-1] = strings.TrimSuffix(last, filepath.Ext(last))
	}

	// Collapse a duplicated TRAILING segment (marker-pdf's one-subdir-per-PDF
	// convention: <X>/<X>). Only the trailing pair, to bound the collision
	// blast radius of the collapse.
	if n := len(cleaned); n >= 2 && cleaned[n-1] == cleaned[n-2] {
		cleaned = cleaned[:n-1]
	}

	joined := strings.Join(cleaned, "-")
	joined = strings.ReplaceAll(joined, ".", "-")
	return joined + ".md"
}

// hasSegmentPrefix reports whether path segments begin with the given prefix
// segments (exact, case-sensitive match per segment).
func hasSegmentPrefix(parts, prefix []string) bool {
	if len(prefix) > len(parts) {
		return false
	}
	for i, seg := range prefix {
		if parts[i] != seg {
			return false
		}
	}
	return true
}

// resolveSourceRoot returns the configured source root (from roots) that most
// specifically prefixes path — the longest segment-prefix match — or "" if none
// applies. Both sides are slash/clean-normalized so comparison is stable; roots
// are matched by whole path segments, not raw string prefixes, so "docs" does
// not match "docsite/...". Issue #107.
func resolveSourceRoot(path string, roots []string) string {
	p := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	best := ""
	bestLen := -1
	for _, root := range roots {
		if root == "" {
			continue
		}
		rNorm := filepath.ToSlash(filepath.Clean(root))
		rParts := strings.Split(rNorm, "/")
		if hasSegmentPrefix(p, rParts) && len(rParts) > bestLen {
			best = rNorm
			bestLen = len(rParts)
		}
	}
	return best
}
