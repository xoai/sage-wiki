package compiler

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
)

func TestResolveWorkerConfig_Defaults(t *testing.T) {
	s := ResolveWorkerConfig(config.WorkerConfig{})
	if s.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", s.PollInterval)
	}
	if s.LeaseTTL != 120*time.Second {
		t.Errorf("LeaseTTL = %v, want 120s", s.LeaseTTL)
	}
	if s.HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval = %v, want 30s", s.HeartbeatInterval)
	}
	if s.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", s.MaxAttempts)
	}
	if s.ClaimLimit != 16 {
		t.Errorf("ClaimLimit = %d, want 16", s.ClaimLimit)
	}
}

func TestResolveWorkerConfig_Overrides(t *testing.T) {
	s := ResolveWorkerConfig(config.WorkerConfig{
		PollIntervalSeconds:      9,
		LeaseTTLSeconds:          300,
		HeartbeatIntervalSeconds: 60,
		MaxAttempts:              3,
		ClaimLimit:               4,
	})
	if s.PollInterval != 9*time.Second || s.LeaseTTL != 300*time.Second ||
		s.HeartbeatInterval != 60*time.Second || s.MaxAttempts != 3 || s.ClaimLimit != 4 {
		t.Errorf("overrides not applied: %+v", s)
	}
}

func openWorkerTestStore(t *testing.T) *CompileItemStore {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCompileItemStore(db, config.NowUTC)
}

func TestWorker_IdleLoop(t *testing.T) {
	items := openWorkerTestStore(t)
	coord := NewCompileCoordinator()
	w := NewWorker(WorkerDeps{
		ProjectDir: t.TempDir(),
		Items:      items,
		Coord:      coord,
		Progress:   NewProgress(),
		Config:     ResolveWorkerConfig(config.WorkerConfig{PollIntervalSeconds: 1}),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Empty queue: nothing claimed, coordinator free between cycles.
	if coord.IsActive() {
		t.Error("coordinator held after idle cycles")
	}
}

func TestWorker_BatchGuardIdles(t *testing.T) {
	items := openWorkerTestStore(t)
	dir := t.TempDir()

	// Pending batch checkpoint in .sage/batch-state.json.
	if err := os.MkdirAll(filepath.Join(dir, ".sage"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkpoint := `{"compile_id":"c1","started_at":"2026-07-24T00:00:00Z","batch":{"batch_id":"b1","provider":"openai","pass":"summarize","submitted_at":"2026-07-24T00:00:00Z","path_by_id":{}},"pending":["a.md"]}`
	if err := os.WriteFile(filepath.Join(dir, ".sage", "batch-state.json"), []byte(checkpoint), 0o644); err != nil {
		t.Fatal(err)
	}

	var processed atomic.Int32
	w := NewWorker(WorkerDeps{
		ProjectDir: dir,
		Items:      items,
		Coord:      NewCompileCoordinator(),
		Progress:   NewProgress(),
		Config:     ResolveWorkerConfig(config.WorkerConfig{}),
		Process: func(ctx context.Context) (bool, error) {
			processed.Add(1)
			return false, nil
		},
	})
	if worked, err := w.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	} else if worked {
		t.Error("cycle worked despite pending batch")
	}
	if processed.Load() != 0 {
		t.Errorf("process invoked %d times with pending batch, want 0", processed.Load())
	}

	// Removing the checkpoint lets the cycle through.
	if err := os.Remove(filepath.Join(dir, ".sage", "batch-state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if processed.Load() != 1 {
		t.Errorf("process invoked %d times without batch, want 1", processed.Load())
	}
}

func TestWorker_RequeueExpiredOnStartup(t *testing.T) {
	items := openWorkerTestStore(t)
	// Plant an expired lease directly through the claim path.
	if err := items.Upsert(CompileItem{SourcePath: "x.md", Hash: "h", Tier: 1, PassIndexed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Claim(1, "dead-worker", -time.Hour, 10); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(WorkerDeps{
		ProjectDir: t.TempDir(),
		Items:      items,
		Coord:      NewCompileCoordinator(),
		Progress:   NewProgress(),
		Config:     ResolveWorkerConfig(config.WorkerConfig{}),
		Process:    func(ctx context.Context) (bool, error) { return false, nil },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = w.Run(ctx)

	got, err := items.GetByPath("x.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.LeaseOwner != "" {
		t.Errorf("expired lease not requeued on startup: %+v", got)
	}
}
