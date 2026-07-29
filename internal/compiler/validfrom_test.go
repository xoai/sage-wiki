package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

func TestValidFromForDoc(t *testing.T) {
	dir := t.TempDir()

	// frontmatter date wins
	dated := filepath.Join(dir, "raw", "dated.md")
	if err := os.MkdirAll(filepath.Dir(dated), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dated, []byte("---\ndate: 2024-03-15\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := validFromForDoc(dir, nil, "raw/dated.md")
	if got != "2024-03-15T00:00:00Z" {
		t.Errorf("frontmatter date: got %q", got)
	}

	// unknown: nonexistent file, no manifest entry → empty, never the epoch
	if got := validFromForDoc(dir, nil, "raw/missing.md"); got != "" {
		t.Errorf("unknown date must stay empty, got %q", got)
	}

	// manifest added-at fallback for a file that cannot be stat'd
	mf := &manifest.Manifest{Sources: map[string]manifest.Source{
		"raw/gone.md": {AddedAt: "2025-06-01T10:00:00Z"},
	}}
	if got := validFromForDoc(dir, mf, "raw/gone.md"); got != "2025-06-01T10:00:00Z" {
		t.Errorf("manifest fallback: got %q", got)
	}
}
