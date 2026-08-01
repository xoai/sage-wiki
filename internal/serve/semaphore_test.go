package serve

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/pkg/engine"
)

// TestSharedCompileSemaphore: the fake exec IS the observation hook
// (scheduled by the plan): it records peak concurrency across all callers.

func TestSharedCompileSemaphore(t *testing.T) {
	const cap, jobs = 2, 8
	sem := make(chan struct{}, cap)
	var cur, maxSeen atomic.Int32
	var wg sync.WaitGroup

	// The fake exec IS the observation hook (scheduled by the plan): it
	// records peak concurrency across all callers.
	exec := semaphoreWrap(sem, func(ctx context.Context, j *Job) (json.RawMessage, error) {
		c := cur.Add(1)
		for {
			m := maxSeen.Load()
			if c <= m || maxSeen.CompareAndSwap(m, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return json.RawMessage(`{}`), nil
	})

	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := exec(context.Background(), &Job{}); err != nil {
				t.Errorf("exec: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := maxSeen.Load(); got > cap {
		t.Errorf("peak concurrency = %d, want ≤ %d", got, cap)
	}
	if got := maxSeen.Load(); got < cap {
		t.Errorf("peak concurrency = %d, want the semaphore actually exercised (≥ %d)", got, cap)
	}
}

func TestSharedCompileSemaphore_Cancel(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // full
	exec := semaphoreWrap(sem, func(ctx context.Context, j *Job) (json.RawMessage, error) {
		t.Error("exec must not run when the ctx is cancelled while gated")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exec(ctx, &Job{}); err == nil {
		t.Error("cancelled gated exec must return ctx.Err")
	}
}

func TestMultiStacksShareSemaphore(t *testing.T) {
	ms := multiFixture(t, nil)
	// Every assembled stack's Server must carry the SAME root semaphore.
	ctx := context.Background()
	stA, err := ms.reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	defer stA.release()
	stB, err := ms.reg.acquire(ctx, "ws-b")
	if err != nil {
		t.Fatal(err)
	}
	defer stB.release()

	semA := stA.srv.cfg.CompileSem
	semB := stB.srv.cfg.CompileSem
	if semA == nil || semB == nil {
		t.Fatal("stacks must carry the root CompileSem")
	}
	// Identity check: same channel (compare via capacity+send interplay is
	// flaky; channels compare by identity in Go).
	if semA != semB {
		t.Error("stacks hold DIFFERENT semaphores — the cross-workspace bound is not shared")
	}
	if cap(semA) != 2 {
		t.Errorf("root semaphore capacity = %d, want 2 (default max-concurrent-compiles)", cap(semA))
	}
}

func TestMultiDrainOrder(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	w, err := engine.Init(ctx, filepath.Join(root, "ws-a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ms, err := NewMulti(ctx, MultiConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	// Open a stack so the Manager holds a lock.
	st, err := ms.reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	st.release()

	if err := ms.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Locks released LAST: after a clean drain a direct open succeeds.
	w2, err := engine.Open(ctx, filepath.Join(root, "ws-a"))
	if err != nil {
		t.Fatalf("workspace lock leaked past drain: %v", err)
	}
	_ = w2.Close()
}
