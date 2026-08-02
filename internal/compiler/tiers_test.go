package compiler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestTierManager_ResolveTier_ConfigDefaults(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.CompilerConfig{
		DefaultTier: 1,
		TierDefaults: map[string]int{
			"json": 0,
			"yaml": 0,
			"md":   1,
			"go":   1,
		},
	}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	tests := []struct {
		path string
		want int
	}{
		{"raw/data.json", 0},
		{"raw/config.yaml", 0},
		{"raw/readme.md", 1},
		{"raw/main.go", 1},
		{"raw/unknown.xyz", 1}, // falls back to default_tier
	}

	for _, tt := range tests {
		got := tm.ResolveTier(tt.path, "/tmp", nil)
		if got != tt.want {
			t.Errorf("ResolveTier(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestTierManager_ResolveTier_Frontmatter(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.CompilerConfig{DefaultTier: 1}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	// Frontmatter overrides config default
	fm := map[string]interface{}{"tier": 3}
	got := tm.ResolveTier("raw/important.md", "/tmp", fm)
	if got != 3 {
		t.Errorf("frontmatter tier=3, got %d", got)
	}

	// Nil frontmatter uses config default
	got = tm.ResolveTier("raw/important.md", "/tmp", nil)
	if got != 1 {
		t.Errorf("nil frontmatter should use config default (1), got %d", got)
	}
}

func TestTierManager_ResolveTier_WikiTier(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	rawDir := filepath.Join(projectDir, "raw")
	os.MkdirAll(rawDir, 0755)

	// Create .wikitier file
	os.WriteFile(filepath.Join(rawDir, ".wikitier"), []byte("\"*.json\": 0\n\"*.md\": 3\n\"README.md\": 0\n"), 0644)

	cfg := &config.CompilerConfig{DefaultTier: 1}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	tests := []struct {
		path string
		want int
	}{
		{"raw/data.json", 0},
		{"raw/article.md", 3},
		{"raw/README.md", 0}, // exact match wins (depends on map iteration order)
	}

	for _, tt := range tests {
		got := tm.ResolveTier(tt.path, projectDir, nil)
		if got != tt.want {
			t.Errorf("ResolveTier(%q) with .wikitier = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestTierManager_ResolveTier_WikiTierParentWalk(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	rawDir := filepath.Join(projectDir, "raw")
	subDir := filepath.Join(rawDir, "docs", "subdir")
	os.MkdirAll(subDir, 0755)

	// Create .wikitier in raw/ (parent) — applies to all nested files
	os.WriteFile(filepath.Join(rawDir, ".wikitier"), []byte("\"*.md\": 3\n"), 0644)

	cfg := &config.CompilerConfig{DefaultTier: 1}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	// File in nested subdir should inherit parent .wikitier
	got := tm.ResolveTier("raw/docs/subdir/deep.md", projectDir, nil)
	if got != 3 {
		t.Errorf("ResolveTier(raw/docs/subdir/deep.md) = %d, want 3 (from parent .wikitier)", got)
	}

	// File at root level with no .wikitier falls back to config default
	got = tm.ResolveTier("toplevel.md", projectDir, nil)
	if got != 1 {
		t.Errorf("ResolveTier(toplevel.md) = %d, want 1 (config default)", got)
	}
}

func TestTierManager_ResolveTier_WikiTierPrecedence(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	rawDir := filepath.Join(projectDir, "raw")
	docsDir := filepath.Join(rawDir, "docs")
	os.MkdirAll(docsDir, 0755)

	// Parent .wikitier: *.md → tier 3
	os.WriteFile(filepath.Join(rawDir, ".wikitier"), []byte("\"*.md\": 3\n"), 0644)
	// Child .wikitier: *.md → tier 0 (overrides parent)
	os.WriteFile(filepath.Join(docsDir, ".wikitier"), []byte("\"*.md\": 0\n"), 0644)

	cfg := &config.CompilerConfig{DefaultTier: 1}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	// File in docs/ should use child .wikitier (tier 0), not parent (tier 3)
	got := tm.ResolveTier("raw/docs/readme.md", projectDir, nil)
	if got != 0 {
		t.Errorf("ResolveTier(raw/docs/readme.md) = %d, want 0 (child .wikitier wins)", got)
	}

	// File in raw/ (not docs/) should use parent .wikitier (tier 3)
	got = tm.ResolveTier("raw/article.md", projectDir, nil)
	if got != 3 {
		t.Errorf("ResolveTier(raw/article.md) = %d, want 3 (parent .wikitier)", got)
	}
}

func TestTierManager_CheckPromotions(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.CompilerConfig{
		DefaultTier: 1,
		PromoteSignals: config.PromoteSignals{
			QueryHitCount: 3,
		},
	}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	// Source with enough hits to promote
	items.Upsert(CompileItem{
		SourcePath: "raw/hot.md", Tier: 1, SourceType: "compiler",
		QueryHitCount: 5,
	})
	// Source without enough hits
	items.Upsert(CompileItem{
		SourcePath: "raw/cold.md", Tier: 1, SourceType: "compiler",
		QueryHitCount: 1,
	})

	promoted, err := tm.CheckPromotions()
	if err != nil {
		t.Fatalf("CheckPromotions: %v", err)
	}
	if len(promoted) != 1 {
		t.Fatalf("expected 1 promoted, got %d", len(promoted))
	}
	if promoted[0] != "raw/hot.md" {
		t.Errorf("expected raw/hot.md promoted, got %s", promoted[0])
	}
}

func TestTierManager_CheckDemotions(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.CompilerConfig{
		DefaultTier: 1,
		DemoteSignals: config.DemoteSignals{
			StaleDays: 90,
		},
	}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	// Dates are relative to now (not hardcoded) so the test can't rot: the SUT
	// demotes anything queried before time.Now().AddDate(0,0,-StaleDays), and
	// StaleDays is 90 here. Same RFC3339 format as the SUT threshold so the
	// store's string comparison is apples-to-apples.
	staleAt := time.Now().AddDate(0, 0, -100).Format(time.RFC3339) // >90d old → demoted
	freshAt := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)  // <90d old → kept

	// Stale source (queried 100 days ago)
	items.Upsert(CompileItem{
		SourcePath: "raw/stale.md", Tier: 3, SourceType: "compiler",
		LastQueriedAt: staleAt,
	})
	// Recent source
	items.Upsert(CompileItem{
		SourcePath: "raw/fresh.md", Tier: 3, SourceType: "compiler",
		LastQueriedAt: freshAt,
	})

	demoted, err := tm.CheckDemotions()
	if err != nil {
		t.Fatalf("CheckDemotions: %v", err)
	}
	if len(demoted) != 1 {
		t.Fatalf("expected 1 demoted, got %d", len(demoted))
	}
	if demoted[0] != "raw/stale.md" {
		t.Errorf("expected raw/stale.md demoted, got %s", demoted[0])
	}
}

func TestTierManager_ConfigDefault(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	cfg := &config.CompilerConfig{
		DefaultTier: 1,
		TierDefaults: map[string]int{
			"json": 0,
			"lock": 0,
		},
	}
	items := NewCompileItemStore(db, config.NowUTC)
	tm := NewTierManager(cfg, items)

	if got := tm.ConfigDefault("data.json"); got != 0 {
		t.Errorf("json = %d, want 0", got)
	}
	if got := tm.ConfigDefault("package-lock.lock"); got != 0 {
		t.Errorf("lock = %d, want 0", got)
	}
	if got := tm.ConfigDefault("readme.md"); got != 1 {
		t.Errorf("md = %d, want 1 (default)", got)
	}
}
