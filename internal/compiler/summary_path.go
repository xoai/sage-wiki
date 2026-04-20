package compiler

import (
	"path/filepath"
	"strings"
)

// SummaryFilename converts a source path to a unique summary filename.
//
// Using only filepath.Base() causes collisions when multiple sources share
// the same basename (e.g. docs/projects/claw/manifest.md and
// docs/projects/workflow/manifest.md both become manifest.md).
//
// This function preserves enough path context to avoid collisions:
//   - "docs/projects/claw/manifest.md"        → "projects-claw-manifest.md"
//   - "docs/projects/workflow/manifest.md"     → "projects-workflow-manifest.md"
//   - "raw/paper.md"                           → "paper.md" (no ambiguity)
//   - "../../ezra/docs/projects/claw/manifest.md" → "projects-claw-manifest.md"
//
// The algorithm: strip common prefixes (../../, docs/), then join remaining
// path segments with hyphens. Single-segment paths (unique basenames) are
// unchanged for backwards compatibility.
func SummaryFilename(sourcePath string) string {
	// Normalize separators
	p := filepath.ToSlash(sourcePath)

	// Strip leading relative path components
	for strings.HasPrefix(p, "../") {
		p = p[3:]
	}

	// Strip common doc root prefixes
	prefixes := []string{
		"docs/",
		"raw/",
	}
	for _, prefix := range prefixes {
		// Also handle <project>/docs/ patterns like "ezra/docs/"
		if idx := strings.Index(p, prefix); idx >= 0 {
			p = p[idx+len(prefix):]
			break
		}
	}

	// Remove file extension
	ext := filepath.Ext(p)
	p = strings.TrimSuffix(p, ext)

	// Split into segments
	parts := strings.Split(p, "/")

	// Single segment — no collision risk, keep as-is for backwards compat
	if len(parts) <= 1 {
		return parts[0] + ".md"
	}

	// Multiple segments — join with hyphens to create unique filename
	return strings.Join(parts, "-") + ".md"
}
