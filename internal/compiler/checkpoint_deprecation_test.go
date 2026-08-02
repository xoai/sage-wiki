package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
)

// openTestProjectDB opens the project's .sage/wiki.db for assertions.
func openTestProjectDB(t *testing.T, projectDir string) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("open project db: %v", err)
	}
	return db
}

// TestCompile_FreshClearsBatchCheckpoints: --fresh clears BOTH checkpoint
// files (batch + legacy), preserving the "clear checkpoint with --fresh"
// recovery the provider-mismatch error message promises (spec D3, test 9).
// Covers the provider-mismatched fixture and the stale non-mismatched one.
func TestCompile_FreshClearsBatchCheckpoints(t *testing.T) {
	t.Run("provider-mismatched legacy batch", func(t *testing.T) {
		fake := newFakeBatchServer(t)
		dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

		writeLegacyState(t, dir, CompileState{
			CompileID: "legacy-1",
			Pending:   []string{"raw/a.md"},
			Batch:     &BatchState{BatchID: "batch_old", Provider: "anthropic", Pass: "summarize"},
		})

		// --fresh: standard compile proceeds, checkpoints deleted, no
		// provider-mismatch error.
		if _, err := Compile(dir, CompileOpts{Fresh: true}); err != nil {
			t.Fatalf("fresh compile: %v", err)
		}
		if _, err := os.Stat(legacyCheckpointPath(dir)); !os.IsNotExist(err) {
			t.Error("legacy compile-state.json not cleared by --fresh")
		}
		if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
			t.Error("batch-state.json should not exist after --fresh")
		}
		if fake.pollCount.Load() != 0 || fake.submitCount.Load() != 0 {
			t.Error("--fresh must neither resume nor submit a batch")
		}

		// Next non-fresh run compiles clean — no provider-mismatch loop.
		if _, err := Compile(dir, CompileOpts{}); err != nil {
			t.Fatalf("post-fresh compile: %v", err)
		}
	})

	t.Run("stale non-mismatched batch file", func(t *testing.T) {
		fake := newFakeBatchServer(t)
		dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

		if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
			Batch:   &BatchState{BatchID: "batch_stale", Provider: "openai", Pass: "summarize"},
			Pending: []string{"raw/a.md"},
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := Compile(dir, CompileOpts{Fresh: true}); err != nil {
			t.Fatalf("fresh compile: %v", err)
		}
		if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
			t.Error("stale batch-state.json not cleared by --fresh")
		}
		if fake.pollCount.Load() != 0 {
			t.Error("--fresh must not resume the stale batch")
		}
	})
}

// TestCompile_DryRunPendingBatch: --dry-run with a pending batch reports it
// and returns WITHOUT polling, writing summaries, or deleting the batch
// checkpoint (spec D3 dry-run contract). The one-time legacy split MAY run.
func TestCompile_DryRunPendingBatch(t *testing.T) {
	t.Run("already-split batch file untouched", func(t *testing.T) {
		fake := newFakeBatchServer(t)
		dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

		if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
			Batch:   &BatchState{BatchID: "batch_test_1", Provider: "openai", Pass: "summarize"},
			Pending: []string{"raw/a.md"},
		}); err != nil {
			t.Fatal(err)
		}

		if _, err := Compile(dir, CompileOpts{DryRun: true}); err != nil {
			t.Fatalf("dry-run compile: %v", err)
		}
		if fake.pollCount.Load() != 0 {
			t.Error("dry-run polled the provider")
		}
		bcp, err := loadBatchCheckpoint(dir)
		if err != nil || bcp == nil || bcp.Batch.BatchID != "batch_test_1" {
			t.Error("batch-state.json modified by dry-run")
		}
		if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-a.md")); !os.IsNotExist(err) {
			t.Error("dry-run wrote a summary")
		}
	})

	t.Run("legacy-only batch: split may run, still no poll", func(t *testing.T) {
		fake := newFakeBatchServer(t)
		dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

		writeLegacyState(t, dir, CompileState{
			CompileID: "legacy-1",
			Pending:   []string{"raw/a.md"},
			Batch:     &BatchState{BatchID: "batch_legacy", Provider: "openai", Pass: "summarize"},
		})

		if _, err := Compile(dir, CompileOpts{DryRun: true}); err != nil {
			t.Fatalf("dry-run compile: %v", err)
		}
		if fake.pollCount.Load() != 0 {
			t.Error("dry-run polled the provider")
		}
		// The one-time split is permitted (idempotent metadata migration):
		// batch-state.json exists, legacy Batch-stripped, nothing else done.
		if _, err := os.Stat(batchCheckpointPath(dir)); err != nil {
			t.Error("one-time split did not run under dry-run")
		}
		legacy := readLegacyState(t, dir)
		if legacy.Batch != nil {
			t.Error("legacy JSON not Batch-stripped by the dry-run split")
		}
		if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-a.md")); !os.IsNotExist(err) {
			t.Error("dry-run wrote a summary")
		}
	})
}

// TestCompile_FailedSourceStaysPending_NoLegacyJSON: a standard compile with
// one source failing writes NO compile-state.json (P1-3); the failed source
// stays pending in compile_items and is retried — successfully — next run
// (resume matrix row 1, failure variant).
func TestCompile_FailedSourceStaysPending_NoLegacyJSON(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md", "raw/b.md")

	// Poison b.md: the summarize prompt embeds the source path; 500 it.
	fake.failPaths.Store("raw/b.md")

	r1, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	if r1.Errors != 1 || r1.Summarized != 1 {
		t.Errorf("first compile: Summarized=%d Errors=%d, want 1/1", r1.Summarized, r1.Errors)
	}
	if _, err := os.Stat(legacyCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("compile-state.json must not be written on failure (P1-3)")
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("batch-state.json must not be written on the standard path")
	}

	// Failed source still pending in compile_items.
	db := openTestProjectDB(t, dir)
	defer db.Close()
	items := NewCompileItemStore(db, config.NowUTC)
	b, err := items.GetByPath("raw/b.md")
	if err != nil || b == nil {
		t.Fatalf("raw/b.md missing from compile_items: %v", err)
	}
	if b.PassSummarized {
		t.Error("failed source marked summarized — resume would skip it")
	}

	// Unpoison; second run retries b.md to completion.
	fake.failPaths.Store("")
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if r2.Summarized != 1 {
		t.Errorf("second compile Summarized = %d, want 1 (b.md retried)", r2.Summarized)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "summaries", "raw-b.md")); err != nil {
		t.Error("raw-b.md summary missing after retry")
	}
}

// TestCompile_FreshDryRunPreservesCheckpoints: --fresh --dry-run must be
// fully side-effect-free (Gate-8 finding): pre-P1-3 it only skipped the
// checkpoint load; deleting a paid in-flight batch ID on a preview command
// is a regression, not a cleanup.
func TestCompile_FreshDryRunPreservesCheckpoints(t *testing.T) {
	fake := newFakeBatchServer(t)
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		Batch:   &BatchState{BatchID: "batch_keep", Provider: "openai", Pass: "summarize"},
		Pending: []string{"raw/a.md"},
	}); err != nil {
		t.Fatal(err)
	}
	writeLegacyState(t, dir, CompileState{CompileID: "legacy-keep", Completed: []string{"raw/old.md"}})

	if _, err := Compile(dir, CompileOpts{Fresh: true, DryRun: true}); err != nil {
		t.Fatalf("fresh+dry-run compile: %v", err)
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); err != nil {
		t.Error("--fresh --dry-run deleted batch-state.json — must be side-effect-free")
	}
	if _, err := os.Stat(legacyCheckpointPath(dir)); err != nil {
		t.Error("--fresh --dry-run deleted compile-state.json — must be side-effect-free")
	}
	if fake.pollCount.Load() != 0 {
		t.Error("--fresh --dry-run polled the provider")
	}

	// A real --fresh (no dry-run) still clears.
	if _, err := Compile(dir, CompileOpts{Fresh: true}); err != nil {
		t.Fatalf("fresh compile: %v", err)
	}
	if _, err := os.Stat(batchCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("real --fresh must clear batch-state.json")
	}
	if _, err := os.Stat(legacyCheckpointPath(dir)); !os.IsNotExist(err) {
		t.Error("real --fresh must clear compile-state.json")
	}
}
