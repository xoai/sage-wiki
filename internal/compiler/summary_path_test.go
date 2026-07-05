package compiler

import "testing"

func TestSummaryFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Basic single-segment paths
		{"manifest.md", "manifest.md"},
		{"paper.md", "paper.md"},

		// Two-segment paths
		{"raw/paper.md", "raw-paper.md"},
		{"raw/2026-04-10_benchmark.md", "raw-2026-04-10_benchmark.md"},
		{"docs/manifest.md", "docs-manifest.md"},

		// Deep paths — the collision case the PR fixes
		{"docs/projects/claw/manifest.md", "docs-projects-claw-manifest.md"},
		{"docs/projects/workflow/manifest.md", "docs-projects-workflow-manifest.md"},
		{"docs/projects/memory/manifest.md", "docs-projects-memory-manifest.md"},

		// Paths that traverse out of the project
		{"../../ezra/docs/projects/claw/manifest.md", "ezra-docs-projects-claw-manifest.md"},
		{"../../ezra/docs/manifest.md", "ezra-docs-manifest.md"},

		// Archive paths
		{"../../ezra/docs/projects/claw/archive/claw-v1-manifest.md", "ezra-docs-projects-claw-archive-claw-v1-manifest.md"},

		// Non-.md extensions: keep the extension as part of the name so
		// "raw/data.txt" doesn't collide with "raw/data.md"
		{"raw/data.txt", "raw-data-txt.md"},
		{"raw/data.md", "raw-data.md"},
		{"raw/image.png", "raw-image-png.md"},

		// Case-insensitive .md detection
		{"raw/README.MD", "raw-README.md"},
		{"raw/notes.Md", "raw-notes.md"},

		// Edge cases
		{"", "summary.md"},
		{".", "summary.md"},
		{"..", "summary.md"},
		{"./paper.md", "paper.md"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SummaryFilename(tt.input)
			if got != tt.want {
				t.Errorf("SummaryFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestSummaryFilename_NoCollisions asserts that a representative set of
// real-world source paths all produce distinct summary filenames. Pulled
// from the original PR's repro plus the adversarial cases I flagged in
// review (mid-path "docs/", cross-extension stems, root-vs-stripped).
func TestSummaryFilename_NoCollisions(t *testing.T) {
	paths := []string{
		// Same basename, different directories (the original PR's case)
		"../../ezra/docs/projects/claw/manifest.md",
		"../../ezra/docs/projects/workflow/manifest.md",
		"../../ezra/docs/projects/memory/manifest.md",
		"../../ezra/docs/manifest.md",
		"../../ezra/docs/projects/claw/archive/claw-v1-manifest.md",

		// Adversarial: "raw/docs/foo.md" must not collide with "raw/foo.md"
		"raw/docs/foo.md",
		"raw/foo.md",

		// Adversarial: same stem, different extension must not collide
		"raw/data.txt",
		"raw/data.md",

		// Adversarial: stripped-prefix shape must not collide with root file
		"docs/manifest.md",
		"manifest.md",
	}

	seen := make(map[string]string)
	for _, p := range paths {
		fn := SummaryFilename(p)
		if prev, ok := seen[fn]; ok {
			t.Errorf("collision: %q and %q both produce %q", prev, p, fn)
		}
		seen[fn] = p
	}
}

// TestSummaryFilenameMode_FullDelegates locks the issue-#107 default: "full"
// (and the empty/unset mode) must be byte-identical to SummaryFilename for
// every input, regardless of the source root passed — the default naming does
// not change existing behavior.
func TestSummaryFilenameMode_FullDelegates(t *testing.T) {
	inputs := []string{
		"manifest.md", "raw/paper.md", "docs/projects/claw/manifest.md",
		"../../ezra/docs/manifest.md", "raw/data.txt", "", "wiki/pdf2md/X/X.md",
	}
	for _, in := range inputs {
		want := SummaryFilename(in)
		if got := SummaryFilenameMode(in, "wiki/pdf2md", "full"); got != want {
			t.Errorf("full mode diverged for %q: %q != %q", in, got, want)
		}
		if got := SummaryFilenameMode(in, "wiki/pdf2md", ""); got != want {
			t.Errorf("empty mode should behave as full for %q: %q != %q", in, got, want)
		}
	}
}

// TestSummaryFilenameMode_Relative covers issue #107 relative naming: source-
// root prefix strip + trailing duplicate-segment collapse, with the edge cases
// the fix-plan review flagged (trim-.md-before-compare, non-trailing dup kept,
// empty-after-strip, absolute/normalized root, non-.md extension).
func TestSummaryFilenameMode_Relative(t *testing.T) {
	tests := []struct{ path, root, want string }{
		{"wiki/pdf2md/Final FS Letter/Final FS Letter.md", "wiki/pdf2md", "Final FS Letter.md"}, // strip + collapse
		{"docs/Report/Report.md", "docs", "Report.md"},                                         // strip + collapse
		{"docs/Report/Summary.md", "docs", "Report-Summary.md"},                                 // strip, no dup
		{"Report/Report.md", "", "Report.md"},                                                   // collapse, no root
		{"a/a/b.md", "", "a-a-b.md"},                                                             // non-trailing dup preserved
		{"other/Report/Report.md", "docs", "other-Report.md"},                                   // root not a prefix → no strip, still collapse
		{"docs/Report.md", "docs", "Report.md"},                                                 // root == parent → stem only
		{"docs", "docs", "summary.md"},                                                          // empty after strip
		{"docs/Report/Report.md", "./docs/", "Report.md"},                                       // root normalized before match
		{"docs/data/data.txt", "docs", "data-data-txt.md"},                                      // non-.md: no trim, no collapse
	}
	for _, tt := range tests {
		if got := SummaryFilenameMode(tt.path, tt.root, "relative"); got != tt.want {
			t.Errorf("SummaryFilenameMode(%q, %q, relative) = %q, want %q", tt.path, tt.root, got, tt.want)
		}
	}
}

func TestResolveSourceRoot(t *testing.T) {
	sources := []string{"docs", "wiki/pdf2md", "wiki"}
	tests := []struct{ path, want string }{
		{"wiki/pdf2md/X/X.md", "wiki/pdf2md"}, // longest-prefix wins over "wiki"
		{"wiki/other/a.md", "wiki"},
		{"docs/a.md", "docs"},
		{"unrelated/a.md", ""},        // no match
		{"docsite/a.md", ""},          // segment match, not raw string prefix
		{"./docs/a.md", "docs"},       // path normalized before match
	}
	for _, tt := range tests {
		if got := resolveSourceRoot(tt.path, sources); got != tt.want {
			t.Errorf("resolveSourceRoot(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
	if got := resolveSourceRoot("docs/a.md", nil); got != "" {
		t.Errorf("nil roots should yield \"\", got %q", got)
	}
}
