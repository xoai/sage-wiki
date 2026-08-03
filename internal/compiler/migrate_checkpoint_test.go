package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
)

func TestMigrateCheckpoint(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	sageDir := filepath.Join(projectDir, ".sage")
	os.MkdirAll(sageDir, 0755)

	// Create a compile-state.json with mixed state
	state := CompileState{
		CompileID: "20260414-120000",
		StartedAt: "2026-04-14T12:00:00Z",
		Pass:      1,
		Completed: []string{"raw/a.md", "raw/b.md"},
		Pending:   []string{"raw/c.md"},
		Failed: []FailedSource{
			{Path: "raw/d.md", Error: "rate limited", Attempts: 3},
		},
	}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(sageDir, "compile-state.json"), data, 0644)

	// Create a manifest with sources
	mf := manifest.New()
	mf.AddSource("raw/a.md", "sha256:aaa", "article", 1000)
	mf.MarkCompiled("raw/a.md", "wiki/summaries/a.md", []string{"concept-a"})
	mf.AddSource("raw/b.md", "sha256:bbb", "article", 2000)
	mf.MarkCompiled("raw/b.md", "wiki/summaries/b.md", []string{"concept-b"})
	mf.AddSource("raw/c.md", "sha256:ccc", "article", 3000)
	// c.md is pending (not compiled)
	mf.AddSource("raw/d.md", "sha256:ddd", "article", 500)
	// d.md failed

	cfg := &config.Config{
		Compiler: config.CompilerConfig{DefaultTier: 1},
	}

	migrated, err := MigrateCheckpoint(projectDir, NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to occur")
	}

	items := NewCompileItemStore(db, config.NowUTC)

	// Verify compiled sources (a.md and b.md)
	a, _ := items.GetByPath("raw/a.md")
	if a == nil {
		t.Fatal("expected raw/a.md in compile_items")
	}
	if a.Tier != 3 {
		t.Errorf("a.md tier = %d, want 3 (compiled)", a.Tier)
	}
	if !a.PassSummarized || !a.PassWritten {
		t.Error("a.md should have all passes complete (compiled status)")
	}

	// Verify pending source (c.md) — in checkpoint pending list
	c, _ := items.GetByPath("raw/c.md")
	if c == nil {
		t.Fatal("expected raw/c.md in compile_items")
	}
	if c.PassSummarized {
		t.Error("c.md should not have pass_summarized (pending)")
	}

	// Verify failed source (d.md) — has error
	d, _ := items.GetByPath("raw/d.md")
	if d == nil {
		t.Fatal("expected raw/d.md in compile_items")
	}
	if d.Error != "rate limited" {
		t.Errorf("d.md error = %q, want 'rate limited'", d.Error)
	}
	if d.ErrorCount != 3 {
		t.Errorf("d.md error_count = %d, want 3", d.ErrorCount)
	}

	// Verify compile-state.json was deleted
	if _, err := os.Stat(filepath.Join(sageDir, "compile-state.json")); !os.IsNotExist(err) {
		t.Error("compile-state.json should be deleted after migration")
	}

	// Verify total count
	count, _ := items.Count()
	if count != 4 {
		t.Errorf("total items = %d, want 4", count)
	}
}

func TestMigrateCheckpoint_NoFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	mf := manifest.New()
	cfg := &config.Config{}

	migrated, err := MigrateCheckpoint(projectDir, NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated {
		t.Error("should return false when no checkpoint exists")
	}
}

// TestMigrateCheckpoint_AfterBatchSplit covers the P1-3 choreography end to
// end: a legacy checkpoint with an in-flight batch is SPLIT by
// loadOrMigrateBatchCheckpoint (batch portion -> batch-state.json, legacy
// Batch-stripped), then MigrateCheckpoint migrates the rest into
// compile_items and deletes the legacy file.
func TestMigrateCheckpoint_AfterBatchSplit(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()

	state := CompileState{
		CompileID: "20260414-120000",
		Pass:      1,
		Completed: []string{"raw/a.md"},
		Pending:   []string{"raw/c.md"},
		Failed:    []FailedSource{{Path: "raw/d.md", Error: "rate limited", Attempts: 3}},
		Batch:     &BatchState{BatchID: "batch_abc", Provider: "anthropic"},
	}
	data, _ := json.Marshal(state)
	sageDir := filepath.Join(projectDir, ".sage")
	os.MkdirAll(sageDir, 0755)
	os.WriteFile(filepath.Join(sageDir, "compile-state.json"), data, 0644)

	// Step 1: the split (runs at the batch-resume check, no DB).
	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if bcp == nil || bcp.Batch == nil || bcp.Batch.BatchID != "batch_abc" {
		t.Fatalf("split returned %+v, want batch_abc", bcp)
	}

	// Step 2: MigrateCheckpoint finishes the job.
	mf := manifest.New()
	mf.AddSource("raw/a.md", "sha256:aaa", "article", 1000)
	mf.MarkCompiled("raw/a.md", "wiki/summaries/a.md", nil)
	mf.AddSource("raw/d.md", "sha256:ddd", "article", 500)
	cfg := &config.Config{}

	migrated, err := MigrateCheckpoint(projectDir, NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Error("expected migration to occur")
	}

	// Legacy JSON gone; batch-state.json survives for the resume.
	if _, err := os.Stat(filepath.Join(sageDir, "compile-state.json")); !os.IsNotExist(err) {
		t.Error("compile-state.json should be deleted after migration")
	}
	if _, err := os.Stat(filepath.Join(sageDir, "batch-state.json")); err != nil {
		t.Error("batch-state.json must survive MigrateCheckpoint (batch still in flight)")
	}

	items := NewCompileItemStore(db, config.NowUTC)
	a, _ := items.GetByPath("raw/a.md")
	if a == nil || !a.PassWritten {
		t.Error("raw/a.md should be fully compiled in compile_items")
	}
	// Failed sources from the split legacy file migrate with error details.
	d, _ := items.GetByPath("raw/d.md")
	if d == nil {
		t.Fatal("raw/d.md missing from compile_items")
	}
	if d.Error != "rate limited" || d.ErrorCount != 3 {
		t.Errorf("raw/d.md error = %q×%d, want 'rate limited'×3", d.Error, d.ErrorCount)
	}
}

// TestMigrateCheckpoint_DefensiveStripUnreachableBatch pins the belt-and-braces
// branch: MigrateCheckpoint encountering Batch != nil (unreachable in practice
// under abort-on-error splitting) strips it and still completes — and does NOT
// rescue the batch (no batch-state.json write: a stranded batch ID must never
// be silently destroyed OR silently resurrected by this path).
func TestMigrateCheckpoint_DefensiveStripUnreachableBatch(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	sageDir := filepath.Join(projectDir, ".sage")
	os.MkdirAll(sageDir, 0755)

	state := CompileState{
		CompileID: "20260414-120000",
		Completed: []string{"raw/a.md"},
		Batch:     &BatchState{BatchID: "batch_abc", Provider: "anthropic"},
	}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(sageDir, "compile-state.json"), data, 0644)

	mf := manifest.New()
	mf.AddSource("raw/a.md", "sha256:aaa", "article", 1000)
	mf.MarkCompiled("raw/a.md", "wiki/summaries/a.md", nil)
	cfg := &config.Config{}

	migrated, err := MigrateCheckpoint(projectDir, NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Error("expected migration to occur")
	}
	if _, err := os.Stat(filepath.Join(sageDir, "compile-state.json")); !os.IsNotExist(err) {
		t.Error("compile-state.json should be deleted after migration")
	}
	if _, err := os.Stat(filepath.Join(sageDir, "batch-state.json")); !os.IsNotExist(err) {
		t.Error("MigrateCheckpoint must not create batch-state.json (no rescue branch)")
	}
}

func TestPopulateFromManifest(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	mf := manifest.New()
	mf.AddSource("raw/a.md", "sha256:aaa", "article", 1000)
	mf.MarkCompiled("raw/a.md", "wiki/summaries/a.md", []string{"concept-a"})
	mf.AddSource("raw/b.json", "sha256:bbb", "data", 500)

	cfg := &config.Config{
		Compiler: config.CompilerConfig{
			DefaultTier: 1,
			TierDefaults: map[string]int{
				"json": 0,
			},
		},
	}

	populated, err := PopulateFromManifest(NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if populated != 2 {
		t.Errorf("populated = %d, want 2", populated)
	}

	items := NewCompileItemStore(db, config.NowUTC)

	// Compiled source should be Tier 3
	a, _ := items.GetByPath("raw/a.md")
	if a.Tier != 3 {
		t.Errorf("a.md tier = %d, want 3", a.Tier)
	}
	if !a.PassWritten {
		t.Error("a.md should have pass_written=true")
	}

	// JSON source should use tier_defaults
	b, _ := items.GetByPath("raw/b.json")
	if b.Tier != 0 {
		t.Errorf("b.json tier = %d, want 0 (from tier_defaults)", b.Tier)
	}

	// Re-running should not duplicate
	populated2, _ := PopulateFromManifest(NewCompileItemStore(db, config.NowUTC), mf, cfg)
	if populated2 != 0 {
		t.Errorf("second populate = %d, want 0 (already exists)", populated2)
	}
}
