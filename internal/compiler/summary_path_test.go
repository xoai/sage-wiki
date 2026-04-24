package compiler

import "testing"

func TestSummaryFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Simple basename — unchanged for backwards compat
		{"raw/paper.md", "paper.md"},
		{"raw/2026-04-10_benchmark.md", "2026-04-10_benchmark.md"},

		// Collision case: same basename, different directories
		{"../../ezra/docs/projects/claw/manifest.md", "projects-claw-manifest.md"},
		{"../../ezra/docs/projects/workflow/manifest.md", "projects-workflow-manifest.md"},
		{"../../ezra/docs/projects/memory/manifest.md", "projects-memory-manifest.md"},
		{"../../ezra/docs/manifest.md", "manifest.md"},

		// Relative paths with docs/ prefix
		{"docs/projects/claw/manifest.md", "projects-claw-manifest.md"},
		{"docs/domains/claw.md", "domains-claw.md"},

		// Archive paths
		{"../../ezra/docs/projects/claw/archive/claw-v1-manifest.md", "projects-claw-archive-claw-v1-manifest.md"},

		// Captures
		{"../../ezra/docs/captures/deer-flow-rejection.md", "captures-deer-flow-rejection.md"},

		// Ideas and research under projects
		{"../../ezra/docs/projects/claw/ideas/2026-04-07_a2a-claw.md", "projects-claw-ideas-2026-04-07_a2a-claw.md"},
		{"../../ezra/docs/projects/claw/research/2026-04-10_nanoclaw-benchmark.md", "projects-claw-research-2026-04-10_nanoclaw-benchmark.md"},

		// Scouting
		{"../../ezra/docs/scouting/2026-04-11.md", "scouting-2026-04-11.md"},

		// Single file at docs root
		{"../../ezra/docs/session-learnings.md", "session-learnings.md"},

		// Non-.md extension — summaries are always .md
		{"raw/data.txt", "data.md"},
		{"raw/image.png", "image.md"},
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

func TestSummaryFilenameNoCollisions(t *testing.T) {
	// These are the actual source paths that collided in production
	paths := []string{
		"../../ezra/docs/projects/claw/manifest.md",
		"../../ezra/docs/projects/workflow/manifest.md",
		"../../ezra/docs/projects/memory/manifest.md",
		"../../ezra/docs/manifest.md",
		"../../ezra/docs/projects/claw/archive/claw-v1-manifest.md",
	}

	seen := make(map[string]string) // filename → source path
	for _, p := range paths {
		fn := SummaryFilename(p)
		if prev, ok := seen[fn]; ok {
			t.Errorf("collision: %q and %q both produce %q", prev, p, fn)
		}
		seen[fn] = p
	}
}
