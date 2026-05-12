package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir
}

func TestDiffDetectsAdded(t *testing.T) {
	dir := setupProject(t)

	// Add source files
	os.WriteFile(filepath.Join(dir, "raw", "article1.md"), []byte("# Article 1\nContent."), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "article2.md"), []byte("# Article 2\nMore content."), 0644)

	cfg, _ := config.Load(filepath.Join(dir, "config.yaml"))
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))

	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Added) != 2 {
		t.Errorf("expected 2 added, got %d", len(diff.Added))
	}
	if len(diff.Modified) != 0 {
		t.Errorf("expected 0 modified, got %d", len(diff.Modified))
	}
	if len(diff.Removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(diff.Removed))
	}
}

func TestDiffDetectsModified(t *testing.T) {
	dir := setupProject(t)

	// Add file and register in manifest
	filePath := filepath.Join(dir, "raw", "article.md")
	os.WriteFile(filePath, []byte("original content"), 0644)

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, _ := manifest.Load(mfPath)

	hash, _ := fileHash(filePath)
	mf.AddSource("raw/article.md", hash, "article", 16)
	mf.MarkCompiled("raw/article.md", "wiki/summaries/article.md", nil)
	mf.Save(mfPath)

	// Modify file
	os.WriteFile(filePath, []byte("modified content!!!"), 0644)

	cfg, _ := config.Load(filepath.Join(dir, "config.yaml"))
	mf, _ = manifest.Load(mfPath)

	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Modified) != 1 {
		t.Errorf("expected 1 modified, got %d", len(diff.Modified))
	}
}

func TestDiffDetectsRemoved(t *testing.T) {
	dir := setupProject(t)

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, _ := manifest.Load(mfPath)
	mf.AddSource("raw/deleted.md", "sha256:old", "article", 100)
	mf.Save(mfPath)

	cfg, _ := config.Load(filepath.Join(dir, "config.yaml"))
	mf, _ = manifest.Load(mfPath)

	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Removed) != 1 {
		t.Errorf("expected 1 removed, got %d", len(diff.Removed))
	}
}

func TestDiffRespectsIgnore(t *testing.T) {
	dir := t.TempDir()

	// Create vault overlay with ignore
	os.MkdirAll(filepath.Join(dir, "Clippings"), 0755)
	os.MkdirAll(filepath.Join(dir, "Personal"), 0755)
	os.WriteFile(filepath.Join(dir, "Clippings", "article.md"), []byte("visible"), 0644)
	os.WriteFile(filepath.Join(dir, "Personal", "diary.md"), []byte("private"), 0644)

	wiki.InitVaultOverlay(dir, "test-vault",
		[]string{"Clippings"},
		[]string{"Personal"},
		"_wiki",
		"gemini-2.5-flash",
	)

	cfg, _ := config.Load(filepath.Join(dir, "config.yaml"))
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))

	diff, _ := Diff(dir, cfg, mf)

	// Only Clippings should be scanned
	for _, s := range diff.Added {
		if s.Path == "Personal/diary.md" {
			t.Error("Personal folder should be ignored")
		}
	}
}

func TestDiff_TypeFromSourceConfig(t *testing.T) {
	dir := setupProject(t)

	// Create an ADR source folder with a markdown file
	adrDir := filepath.Join(dir, "raw", "adrs")
	if err := os.MkdirAll(adrDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adrDir, "decision-001.md"), []byte("# ADR 001\nDecided."), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Build a config with raw/adrs declared as type=adr
	cfg := &config.Config{
		Sources: []config.Source{
			{Path: "raw/adrs", Type: "adr"},
		},
		Output: "wiki",
	}

	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(diff.Added))
	}
	if diff.Added[0].Type != "adr" {
		t.Errorf("Type = %q, want %q (configured source type should override .md → article default)", diff.Added[0].Type, "adr")
	}
}

func TestDiff_TypeFromSourceConfig_AutoFallsBack(t *testing.T) {
	dir := setupProject(t)

	docsDir := filepath.Join(dir, "raw", "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "guide.md"), []byte("# Guide\nContent."), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{
		Sources: []config.Source{
			{Path: "raw/docs", Type: "auto"},
		},
		Output: "wiki",
	}

	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(diff.Added))
	}
	if diff.Added[0].Type != "article" {
		t.Errorf("Type = %q, want %q (auto should fall back to extension detection)", diff.Added[0].Type, "article")
	}
}

// TestDiff_TypeChangeFlagsModified verifies that when a user adds or changes
// cfg.Sources[].type in config.yaml without modifying file contents, the next
// compile detects the affected files as Modified — so they get re-summarized
// with the new type instead of being skipped because their hash is unchanged.
func TestDiff_TypeChangeFlagsModified(t *testing.T) {
	dir := setupProject(t)

	specDir := filepath.Join(dir, "raw", "specs")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath := filepath.Join(specDir, "spec-001.md")
	if err := os.WriteFile(filePath, []byte("# Spec 001"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Prime the manifest with the file already compiled at the OLD type
	hash, _ := fileHash(filePath)
	mfPath := filepath.Join(dir, ".manifest.json")
	mf, _ := manifest.Load(mfPath)
	mf.AddSource("raw/specs/spec-001.md", hash, "article", 10)
	mf.Save(mfPath)
	mf, _ = manifest.Load(mfPath)

	// User has now added `type: spec` to config — file contents unchanged
	cfg := &config.Config{
		Sources: []config.Source{
			{Path: "raw/specs", Type: "spec"},
		},
		Output: "wiki",
	}

	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	// File hash is unchanged but the resolved type is now "spec" (vs cached "article")
	// → must land in Modified so it gets re-summarized with the new type.
	if len(diff.Modified) != 1 {
		t.Fatalf("expected 1 Modified, got %d (Added=%d Removed=%d)",
			len(diff.Modified), len(diff.Added), len(diff.Removed))
	}
	if diff.Modified[0].Type != "spec" {
		t.Errorf("Modified[0].Type = %q, want %q", diff.Modified[0].Type, "spec")
	}
	if diff.Modified[0].Hash != hash {
		t.Errorf("Modified[0].Hash = %q, expected unchanged %q", diff.Modified[0].Hash, hash)
	}
}

// TestDiff_NoChangeWhenTypeMatches confirms files are NOT re-flagged when both
// hash and type match the manifest — guards against trivial-modification noise.
func TestDiff_NoChangeWhenTypeMatches(t *testing.T) {
	dir := setupProject(t)

	specDir := filepath.Join(dir, "raw", "specs")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath := filepath.Join(specDir, "spec-001.md")
	if err := os.WriteFile(filePath, []byte("# Spec 001"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	hash, _ := fileHash(filePath)
	mfPath := filepath.Join(dir, ".manifest.json")
	mf, _ := manifest.Load(mfPath)
	mf.AddSource("raw/specs/spec-001.md", hash, "spec", 10)
	mf.Save(mfPath)
	mf, _ = manifest.Load(mfPath)

	cfg := &config.Config{
		Sources: []config.Source{
			{Path: "raw/specs", Type: "spec"},
		},
		Output: "wiki",
	}

	diff, err := Diff(dir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Added)+len(diff.Modified) != 0 {
		t.Errorf("expected no Added/Modified when hash AND type match, got Added=%d Modified=%d",
			len(diff.Added), len(diff.Modified))
	}
}

func TestIsIgnored(t *testing.T) {
	tests := []struct {
		path    string
		ignore  []string
		want    bool
	}{
		// Prefix match
		{"Personal/diary.md", []string{"Personal"}, true},
		{"Clippings/article.md", []string{"Personal"}, false},
		{"_wiki/concepts/test.md", []string{"_wiki"}, true},
		{"raw/article.md", []string{"Personal", "Templates"}, false},
		// Nested folder match
		{"raw/project/assets/image.png", []string{"assets"}, true},
		// Trailing segment match
		{"raw/project/assets", []string{"assets"}, true},
		// Glob extension match
		{"raw/photo.png", []string{"*.png"}, true},
		// Glob case-insensitive
		{"raw/photo.PNG", []string{"*.png"}, true},
		// Glob no-match
		{"raw/something.md", []string{"*.png"}, false},
		// Partial name should NOT match (regression guard)
		{"raw/biology.md", []string{"log"}, false},
	}
	for _, tt := range tests {
		got := isIgnored(tt.path, tt.ignore)
		if got != tt.want {
			t.Errorf("isIgnored(%q, %v) = %v, want %v", tt.path, tt.ignore, got, tt.want)
		}
	}
}
