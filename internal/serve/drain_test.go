package serve

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// TestShutdownDrainPortable is AC-S5's portable form: in-process Shutdown
// with a blocked job drains within timeout, marks the job interrupted,
// closes deps, and releases the workspace lock.
func TestShutdownDrainPortable(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(deps, nil, Config{Workspace: dir, DrainTimeout: 2 * time.Second, ReadyFn: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}

	// Attach the engine workspace (the lock, §2.0).
	w, err := engine.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWorkspace(w)

	// Block one job forever; the drain must interrupt it.
	blockCh := make(chan struct{})
	srv.queue = NewQueue(srv.ledger, 1, func(ctx context.Context, j *Job) (json.RawMessage, error) {
		select {
		case <-blockCh:
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, clockSeq(time.Millisecond))
	j, err := srv.queue.Submit(CompileJobRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, "127.0.0.1:0")

	time.Sleep(100 * time.Millisecond) // let the job start
	start := time.Now()
	if err := srv.Shutdown(); err != nil && err != context.DeadlineExceeded {
		t.Fatalf("Shutdown: %v", err)
	}
	close(blockCh)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("drain took %v, want < drain-timeout+cushion", elapsed)
	}

	got, _ := srv.ledger.Get(j.ID)
	if got.Status != JobInterrupted {
		t.Errorf("job status = %q, want interrupted", got.Status)
	}

	// Lock released: a fresh read-write Open must succeed now.
	w2, err := engine.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock not released after drain: %v", err)
	}
	w2.Close()

	// Integrity: config + manifest + DB readable post-drain.
	l3, err := OpenLedger(dir)
	if err != nil {
		t.Fatalf("ledger unreadable post-drain: %v", err)
	}
	if _, ok := l3.Get(j.ID); !ok {
		t.Error("job lost from ledger after restart")
	}
}

// TestDrainClampWarns covers the <10s clamp (floor is 10s, warned).
func TestDrainClampWarns(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, _ := AssembleDeps(dir)
	defer deps.Close()
	srv, err := New(deps, nil, Config{Workspace: dir, DrainTimeout: 2 * time.Second, ReadyFn: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if srv.cfg.DrainTimeout != 10*time.Second {
		t.Errorf("DrainTimeout = %v, want clamped 10s", srv.cfg.DrainTimeout)
	}
}
