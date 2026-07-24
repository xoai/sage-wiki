package compiler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/manifest"
)

func TestWorker_EnqueueDiscoversNewSource(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	// No items in the store — the worker's own scan must discover it.
	h.writeSource(t, "new.md", "# New\n\nA source added while serving.")

	worked, err := h.worker.cycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !worked {
		t.Fatal("worker did not discover the new source")
	}
	got, err := h.items.GetByPath("raw/new.md")
	if err != nil || got == nil {
		t.Fatalf("item not upserted by scan: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("status = %q (error %q), want done", got.Status, got.Error)
	}
}

func TestWorker_ManifestSavedOnCompleteCycle(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content for the full pipeline.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	mf, err := manifest.Load(filepath.Join(h.dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if mf.SourceCount() != 1 {
		t.Errorf("manifest sources = %d, want 1 (saved after complete cycle)", mf.SourceCount())
	}
}

func TestWorker_ManifestSkippedOnIncompleteCycle(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	h.worker.hooks.fullPipeline = func(sources []SourceInfo, opts FullPipelineOpts) *FullPipelineResult {
		panic("simulated tier-3 crash")
	}
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	mf, err := manifest.Load(filepath.Join(h.dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if mf.SourceCount() != 0 {
		t.Errorf("manifest sources = %d, want 0 (incomplete cycle must not save)", mf.SourceCount())
	}
}

func TestWorker_RemovedSourcePruned(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	// Item exists in the store AND in the manifest, but the file is gone.
	h.writeSource(t, "gone.md", "# Gone\n\nWas here.")
	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("setup cycle: %v", err)
	}
	// Force the manifest to know the source (tier-1 doesn't AddSource —
	// write it directly, mirroring what a tier-3 compile would persist).
	mfPath := filepath.Join(h.dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		t.Fatal(err)
	}
	mf.AddSource("raw/gone.md", "h1", "md", 10)
	if err := mf.Save(mfPath); err != nil {
		t.Fatal(err)
	}

	// Delete the file, then create a summary file that prune=true would
	// remove — proving prune=false keeps it.
	if err := os.Remove(filepath.Join(h.dir, "raw", "gone.md")); err != nil {
		t.Fatal(err)
	}
	summaryDir := filepath.Join(h.dir, "wiki", "summaries")
	os.MkdirAll(summaryDir, 0o755)
	summaryPath := filepath.Join(summaryDir, "gone.md")
	os.WriteFile(summaryPath, []byte("# summary"), 0o644)

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if got, _ := h.items.GetByPath("raw/gone.md"); got != nil {
		t.Errorf("removed source still in queue: %+v", got)
	}
	if _, err := os.Stat(summaryPath); err != nil {
		t.Errorf("prune=false must not delete output files: %v", err)
	}
}

func TestWorker_PromotionsRun(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	h.writeSource(t, "hot.md", "# Hot\n\nFrequently queried.")
	// Seed: tier-1 item with enough query hits to promote (threshold 3 —
	// IncrementQueryHits dedupes paths per call, so three calls).
	if err := h.items.Upsert(CompileItem{SourcePath: "raw/hot.md", Hash: "h1", FileType: "md", Tier: 1, PassIndexed: true, PassEmbedded: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := h.items.IncrementQueryHits([]string{"raw/hot.md"}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	got, _ := h.items.GetByPath("raw/hot.md")
	if got.Tier != 3 {
		t.Errorf("tier = %d, want 3 (promoted by the worker sweep)", got.Tier)
	}
}

func TestWorker_RemovedSourceDeferredOrphanKeepsRow(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)

	// Manifest knows the source AND a concept whose ONLY source it is —
	// handleRemovedSources(prune=false) defers removal, and the queue row
	// must survive the deferral (F-032 regression).
	h.writeSource(t, "sole.md", "# Sole\n\nOnly source of a concept.")
	mfPath := filepath.Join(h.dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		t.Fatal(err)
	}
	mf.AddSource("raw/sole.md", "h1", "md", 10)
	mf.AddConcept("sole-concept", "wiki/concepts/sole.md", []string{"raw/sole.md"})
	if err := mf.Save(mfPath); err != nil {
		t.Fatal(err)
	}
	if err := h.items.Upsert(CompileItem{SourcePath: "raw/sole.md", Hash: "h1", FileType: "md", Tier: 1, PassIndexed: true, PassEmbedded: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.dir, "raw", "sole.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	got, _ := h.items.GetByPath("raw/sole.md")
	if got == nil {
		t.Fatal("queue row deleted for deferred sole-source orphan — must survive")
	}
}

func TestWorker_FailStreakResetsOnIdle(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.newWorker(t, 50*time.Millisecond, time.Second, 5)
	h.worker.failStreak = 3

	// Idle cycle (nothing to claim) decays the backoff.
	if worked, err := h.worker.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	} else if worked {
		t.Fatal("expected idle cycle")
	}
	if h.worker.failStreak != 0 {
		t.Errorf("failStreak = %d after idle cycle, want 0", h.worker.failStreak)
	}
}
