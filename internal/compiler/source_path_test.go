package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
)

func TestResolveSourcePathKeepsExternalAbsolutePath(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	external := filepath.Join(t.TempDir(), "external", "note.md")

	if got := resolveSourcePath(projectDir, external); got != filepath.Clean(external) {
		t.Fatalf("resolveSourcePath external = %q, want %q", got, filepath.Clean(external))
	}
}

func TestResolveSourcePathResolvesProjectRelativePath(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "project")
	want := filepath.Join(projectDir, "raw", "note.md")

	if got := resolveSourcePath(projectDir, filepath.Join("raw", "note.md")); got != want {
		t.Fatalf("resolveSourcePath relative = %q, want %q", got, want)
	}
}

func TestDiff_ExternalRootsSameRelativePathRemainIndependent(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "project")
	rootA := filepath.Join(base, "A")
	rootB := filepath.Join(base, "B")
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(filepath.Join(root, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pathA := filepath.Join(rootA, "subdir", "spec.pdf")
	pathB := filepath.Join(rootB, "subdir", "spec.pdf")
	if err := os.WriteFile(pathA, []byte("root A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("root B"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Sources: []config.Source{
		{Path: rootA, Type: "auto", ReadOnly: true},
		{Path: rootB, Type: "auto", ReadOnly: true},
	}}
	diff, err := Diff(projectDir, cfg, manifest.New())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 2 {
		t.Fatalf("expected two independent sources, got %d", len(diff.Added))
	}

	seen := make(map[string]string, len(diff.Added))
	for _, info := range diff.Added {
		physical := resolveSourcePath(projectDir, info.Path)
		hash, err := fileHash(physical)
		if err != nil {
			t.Fatalf("hash %q (%q): %v", info.Path, physical, err)
		}
		seen[info.Path] = hash
	}
	if len(seen) != 2 {
		t.Fatalf("same relative path collapsed into one source identity: %#v", seen)
	}
	if seen[filepath.ToSlash(filepath.Join("..", "A", "subdir", "spec.pdf"))] == seen[filepath.ToSlash(filepath.Join("..", "B", "subdir", "spec.pdf"))] {
		t.Fatal("different external roots unexpectedly share the same content identity")
	}
}
