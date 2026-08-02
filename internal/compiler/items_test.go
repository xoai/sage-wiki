package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) (*storage.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db, func() {
		db.Close()
		os.RemoveAll(dir)
	}
}

func TestCompileItemStore_UpsertAndGet(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)

	item := CompileItem{
		SourcePath:  "raw/docs/test.md",
		Hash:        "sha256:abc123",
		FileType:    "article",
		SizeBytes:   1024,
		Tier:        1,
		TierDefault: 1,
		SourceType:  "compiler",
	}

	// Insert
	if err := store.Upsert(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Get
	got, err := store.GetByPath("raw/docs/test.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected item, got nil")
	}
	if got.Hash != "sha256:abc123" {
		t.Errorf("hash = %s, want sha256:abc123", got.Hash)
	}
	if got.Tier != 1 {
		t.Errorf("tier = %d, want 1", got.Tier)
	}
	if got.SourceType != "compiler" {
		t.Errorf("source_type = %s, want compiler", got.SourceType)
	}

	// Update (upsert existing)
	item.Hash = "sha256:def456"
	item.PassIndexed = true
	if err := store.Upsert(item); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	got, _ = store.GetByPath("raw/docs/test.md")
	if got.Hash != "sha256:def456" {
		t.Errorf("updated hash = %s, want sha256:def456", got.Hash)
	}
	if !got.PassIndexed {
		t.Error("expected pass_indexed=true after upsert")
	}
}

func TestCompileItemStore_GetByPath_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	got, err := store.GetByPath("nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent path, got %+v", got)
	}
}

func TestCompileItemStore_ListByTier(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)

	for i := 0; i < 5; i++ {
		store.Upsert(CompileItem{
			SourcePath: fmt.Sprintf("raw/t0_%d.json", i),
			Tier:       0, TierDefault: 0, SourceType: "compiler",
		})
	}
	for i := 0; i < 3; i++ {
		store.Upsert(CompileItem{
			SourcePath: fmt.Sprintf("raw/t1_%d.md", i),
			Tier:       1, TierDefault: 1, SourceType: "compiler",
		})
	}

	tier0, err := store.ListByTier(0)
	if err != nil {
		t.Fatalf("list tier 0: %v", err)
	}
	if len(tier0) != 5 {
		t.Errorf("tier 0 count = %d, want 5", len(tier0))
	}

	tier1, _ := store.ListByTier(1)
	if len(tier1) != 3 {
		t.Errorf("tier 1 count = %d, want 3", len(tier1))
	}
}

func TestCompileItemStore_ListPending(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)

	// Tier 1 source, not yet embedded
	store.Upsert(CompileItem{
		SourcePath: "raw/a.md", Tier: 1, TierDefault: 1,
		PassIndexed: true, SourceType: "compiler",
	})
	// Tier 1 source, fully done at tier 1
	store.Upsert(CompileItem{
		SourcePath: "raw/b.md", Tier: 1, TierDefault: 1,
		PassIndexed: true, PassEmbedded: true, SourceType: "compiler",
	})
	// Tier 3 source, needs writing
	store.Upsert(CompileItem{
		SourcePath: "raw/c.md", Tier: 3, TierDefault: 1,
		PassIndexed: true, PassEmbedded: true,
		PassSummarized: true, PassExtracted: true, SourceType: "compiler",
	})

	pending1, _ := store.ListPending(1)
	if len(pending1) != 1 {
		t.Errorf("pending tier 1 = %d, want 1 (a.md needs embedding)", len(pending1))
	}
	if len(pending1) > 0 && pending1[0].SourcePath != "raw/a.md" {
		t.Errorf("expected raw/a.md, got %s", pending1[0].SourcePath)
	}

	pending3, _ := store.ListPending(3)
	if len(pending3) != 1 {
		t.Errorf("pending tier 3 = %d, want 1 (c.md needs writing)", len(pending3))
	}
}

func TestCompileItemStore_MarkPass(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{
		SourcePath: "raw/test.md", Tier: 1, SourceType: "compiler",
	})

	if err := store.MarkPass("raw/test.md", "indexed"); err != nil {
		t.Fatalf("mark pass: %v", err)
	}

	got, _ := store.GetByPath("raw/test.md")
	if !got.PassIndexed {
		t.Error("expected pass_indexed=true after MarkPass")
	}

	// Invalid pass name
	if err := store.MarkPass("raw/test.md", "invalid"); err == nil {
		t.Error("expected error for invalid pass name")
	}
}

func TestCompileItemStore_SetTier_Idempotent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{
		SourcePath: "raw/test.md", Tier: 1, TierDefault: 1,
		PassIndexed: true, PassEmbedded: true, SourceType: "compiler",
	})

	// Promote to tier 3 — should retain pass flags
	if err := store.SetTier("raw/test.md", 3, "on-demand"); err != nil {
		t.Fatalf("set tier: %v", err)
	}

	got, _ := store.GetByPath("raw/test.md")
	if got.Tier != 3 {
		t.Errorf("tier = %d, want 3", got.Tier)
	}
	if !got.PassIndexed {
		t.Error("pass_indexed should be retained after promotion")
	}
	if !got.PassEmbedded {
		t.Error("pass_embedded should be retained after promotion")
	}
	if got.PromotedAt == "" {
		t.Error("promoted_at should be set")
	}
}

func TestCompileItemStore_IncrementQueryHits(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{
		SourcePath: "raw/test.md", Tier: 1, SourceType: "compiler",
	})

	if err := store.IncrementQueryHits([]string{"raw/test.md"}); err != nil {
		t.Fatalf("increment: %v", err)
	}
	if err := store.IncrementQueryHits([]string{"raw/test.md"}); err != nil {
		t.Fatalf("increment: %v", err)
	}

	got, _ := store.GetByPath("raw/test.md")
	if got.QueryHitCount != 2 {
		t.Errorf("query_hit_count = %d, want 2", got.QueryHitCount)
	}
	if got.LastQueriedAt == "" {
		t.Error("last_queried_at should be set")
	}
}

func TestCompileItemStore_IncrementQueryHits_MultiplePaths(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{SourcePath: "raw/a.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "raw/b.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "raw/c.md", Tier: 1, SourceType: "compiler"})

	// Batch increment of multiple paths in one call
	if err := store.IncrementQueryHits([]string{"raw/a.md", "raw/b.md", "raw/c.md"}); err != nil {
		t.Fatalf("batch increment: %v", err)
	}

	for _, p := range []string{"raw/a.md", "raw/b.md", "raw/c.md"} {
		got, _ := store.GetByPath(p)
		if got.QueryHitCount != 1 {
			t.Errorf("%s query_hit_count = %d, want 1", p, got.QueryHitCount)
		}
		if got.LastQueriedAt == "" {
			t.Errorf("%s last_queried_at should be set", p)
		}
	}

	// Second batch increment — only a and c
	if err := store.IncrementQueryHits([]string{"raw/a.md", "raw/c.md"}); err != nil {
		t.Fatalf("batch increment 2: %v", err)
	}

	a, _ := store.GetByPath("raw/a.md")
	b, _ := store.GetByPath("raw/b.md")
	c, _ := store.GetByPath("raw/c.md")
	if a.QueryHitCount != 2 {
		t.Errorf("a hit count = %d, want 2", a.QueryHitCount)
	}
	if b.QueryHitCount != 1 {
		t.Errorf("b hit count = %d, want 1 (not in second batch)", b.QueryHitCount)
	}
	if c.QueryHitCount != 2 {
		t.Errorf("c hit count = %d, want 2", c.QueryHitCount)
	}
}

func TestCompileItemStore_MarkError(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{
		SourcePath: "raw/test.md", Tier: 1, SourceType: "compiler",
	})

	store.MarkError("raw/test.md", fmt.Errorf("rate limited"))
	store.MarkError("raw/test.md", fmt.Errorf("timeout"))

	got, _ := store.GetByPath("raw/test.md")
	if got.ErrorCount != 2 {
		t.Errorf("error_count = %d, want 2", got.ErrorCount)
	}
	if got.Error != "timeout" {
		t.Errorf("error = %s, want timeout", got.Error)
	}
}

func TestCompileItemStore_Stats(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{SourcePath: "a.json", Tier: 0, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "b.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "c.md", Tier: 3, PassWritten: true, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "d.md", Tier: 1, SourceType: "scribe"})

	stats, err := store.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalSources != 4 {
		t.Errorf("total = %d, want 4", stats.TotalSources)
	}
	if stats.ByTier[0] != 1 {
		t.Errorf("tier 0 = %d, want 1", stats.ByTier[0])
	}
	if stats.ByTier[1] != 2 {
		t.Errorf("tier 1 = %d, want 2", stats.ByTier[1])
	}
	if stats.FullyCompiled != 1 {
		t.Errorf("fully compiled = %d, want 1", stats.FullyCompiled)
	}
	if stats.BySourceType["scribe"] != 1 {
		t.Errorf("scribe count = %d, want 1", stats.BySourceType["scribe"])
	}
}

func TestCompileItemStore_DeleteByPaths(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{SourcePath: "a.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "b.md", Tier: 1, SourceType: "compiler"})

	if err := store.DeleteByPaths([]string{"a.md"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	count, _ := store.Count()
	if count != 1 {
		t.Errorf("count after delete = %d, want 1", count)
	}
}

func TestCompileItemStore_DeleteByPaths_MultiplePaths(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, config.NowUTC)
	store.Upsert(CompileItem{SourcePath: "a.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "b.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "c.md", Tier: 1, SourceType: "compiler"})
	store.Upsert(CompileItem{SourcePath: "d.md", Tier: 1, SourceType: "compiler"})

	// Batch delete of multiple paths in one call
	if err := store.DeleteByPaths([]string{"a.md", "c.md", "d.md"}); err != nil {
		t.Fatalf("batch delete: %v", err)
	}

	count, _ := store.Count()
	if count != 1 {
		t.Errorf("count after batch delete = %d, want 1", count)
	}

	// Verify b.md is the survivor
	got, _ := store.GetByPath("b.md")
	if got == nil {
		t.Error("b.md should still exist after batch delete")
	}

	// Verify deleted items are gone
	for _, p := range []string{"a.md", "c.md", "d.md"} {
		got, _ := store.GetByPath(p)
		if got != nil {
			t.Errorf("%s should have been deleted", p)
		}
	}
}

// TestUpsert_PreservesPassFlags_WhenHashUnchanged verifies that an interrupted
// compile can resume without redoing completed tiers. When the next compile
// re-runs Diff() and calls Upsert with zeroed pass flags (the bare struct
// constructed at pipeline.go:262), the existing pass flags must be preserved
// as long as the hash hasn't changed. Issue #88.
func TestUpsert_PreservesPassFlags_WhenHashUnchanged(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewCompileItemStore(db, config.NowUTC)

	// Simulate a previous compile that completed tier 0 + tier 1
	if err := store.Upsert(CompileItem{
		SourcePath:   "raw/docs/test.md",
		Hash:         "sha256:abc",
		FileType:     "article",
		Tier:         1,
		PassIndexed:  true,
		PassEmbedded: true,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// Simulate next compile after interrupt: Diff re-sees the source as
	// "added" and the caller constructs a bare CompileItem with zero pass flags.
	// Same hash → pass flags must be preserved.
	if err := store.Upsert(CompileItem{
		SourcePath: "raw/docs/test.md",
		Hash:       "sha256:abc",
		FileType:   "article",
		Tier:       1,
		// PassIndexed and PassEmbedded NOT set → both false
	}); err != nil {
		t.Fatalf("resume upsert: %v", err)
	}

	got, err := store.GetByPath("raw/docs/test.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.PassIndexed {
		t.Error("pass_indexed should be preserved across resume with same hash")
	}
	if !got.PassEmbedded {
		t.Error("pass_embedded should be preserved across resume with same hash")
	}
}

// TestUpsert_ResetsPassFlags_WhenHashChanged verifies that a modified source
// (different hash) correctly re-processes through every pass — the sticky
// pass flag behavior must NOT mask genuine file changes.
func TestUpsert_ResetsPassFlags_WhenHashChanged(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewCompileItemStore(db, config.NowUTC)

	if err := store.Upsert(CompileItem{
		SourcePath:     "raw/docs/test.md",
		Hash:           "sha256:original",
		FileType:       "article",
		Tier:           3,
		PassIndexed:    true,
		PassEmbedded:   true,
		PassSummarized: true,
		PassExtracted:  true,
		PassWritten:    true,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// User edits the file → hash changes; caller passes zero pass flags so
	// all passes are re-run from scratch.
	if err := store.Upsert(CompileItem{
		SourcePath: "raw/docs/test.md",
		Hash:       "sha256:modified",
		FileType:   "article",
		Tier:       3,
	}); err != nil {
		t.Fatalf("modified upsert: %v", err)
	}

	got, _ := store.GetByPath("raw/docs/test.md")
	if got.Hash != "sha256:modified" {
		t.Errorf("hash = %q, want updated hash", got.Hash)
	}
	if got.PassIndexed || got.PassEmbedded || got.PassSummarized || got.PassExtracted || got.PassWritten {
		t.Errorf("all pass flags should reset when hash changes; got indexed=%v embedded=%v summarized=%v extracted=%v written=%v",
			got.PassIndexed, got.PassEmbedded, got.PassSummarized, got.PassExtracted, got.PassWritten)
	}
}

// TestUpsert_NewRowGetsPassFlagsFromValues verifies the INSERT path (no
// existing row): pass flags come straight from the inserted values.
func TestUpsert_NewRowGetsPassFlagsFromValues(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewCompileItemStore(db, config.NowUTC)

	if err := store.Upsert(CompileItem{
		SourcePath:  "raw/docs/new.md",
		Hash:        "sha256:fresh",
		FileType:    "article",
		Tier:        1,
		PassIndexed: true,
	}); err != nil {
		t.Fatalf("insert upsert: %v", err)
	}

	got, _ := store.GetByPath("raw/docs/new.md")
	if !got.PassIndexed {
		t.Error("INSERT path: pass_indexed should reflect inserted value")
	}
	if got.PassEmbedded {
		t.Error("INSERT path: pass_embedded should be false (not set in insert)")
	}
}
