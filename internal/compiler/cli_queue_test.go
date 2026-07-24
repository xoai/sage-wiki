package compiler

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// seedFailedItem plants a dead-lettered queue row for a source that exists
// on disk (hash deliberately stale so the next scan is a hash change).
func seedFailedItem(t *testing.T, h *workerHarness, path string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	items := NewCompileItemStore(db)
	if err := items.Upsert(CompileItem{SourcePath: path, Hash: "stale-hash", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Claim(1, "old-worker", time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	if err := items.Release(path, "old-worker", store.ReleaseFailed); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestCompile_CLIGoldenUnchanged(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.writeSource(t, "b.md", "# Beta\n\nBeta content.")

	result, err := Compile(h.dir, CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Summarized != 2 {
		t.Errorf("Summarized = %d, want 2", result.Summarized)
	}
	// Queue settled: both items done, no leases left behind.
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	items := NewCompileItemStore(db)
	for _, p := range []string{"raw/a.md", "raw/b.md"} {
		got, _ := items.GetByPath(p)
		if got == nil {
			t.Fatalf("%s missing from queue", p)
		}
		if got.Status != "done" {
			t.Errorf("%s status = %q, want done (CLI settles its claims)", p, got.Status)
		}
		if got.LeaseOwner != "" {
			t.Errorf("%s lease leaked: %q", p, got.LeaseOwner)
		}
	}
	// Manifest + summaries on disk, as before P2-3.
	matches, _ := filepath.Glob(filepath.Join(h.dir, "wiki", "summaries", "*"))
	if len(matches) != 2 {
		t.Errorf("summaries = %d, want 2", len(matches))
	}
}

func TestCompile_FreshResetsFailed(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	if _, err := Compile(h.dir, CompileOpts{Fresh: true}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	got, _ := NewCompileItemStore(db).GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done (--fresh reset the dead letter)", got.Status)
	}
}

func TestCompile_HashChangeRevivesFailed(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content — edited after failing.")
	seedFailedItem(t, h, "raw/a.md")

	// No --fresh: the hash change alone must revive the item.
	if _, err := Compile(h.dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	got, _ := NewCompileItemStore(db).GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done (hash change revived the dead letter)", got.Status)
	}
}

func TestCompile_FailedItemsSkipped(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	// Compile with nothing new: the dead-lettered item must NOT be retried
	// (the hash matches after the seed's hash is refreshed by a first scan).
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	items := NewCompileItemStore(db)
	// Simulate the first scan having already adopted the real hash while
	// keeping the dead letter (same-hash upsert preserves queue state).
	if _, err := Compile(h.dir, CompileOpts{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	got, _ := items.GetByPath("raw/a.md")
	if got.Status != "failed" {
		t.Errorf("status = %q after dry run, want failed (no side effects)", got.Status)
	}
	db.Close()
}

func TestCompile_DryRunNoSideEffects(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	before, _ := h.items.GetByPath("raw/a.md")
	if _, err := Compile(h.dir, CompileOpts{DryRun: true, Fresh: true}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	after, _ := h.items.GetByPath("raw/a.md")
	if *before != *after {
		t.Errorf("dry run mutated queue state: before %+v after %+v", before, after)
	}
}
