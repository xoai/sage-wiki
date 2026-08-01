package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	// Doctor validates live LLM connectivity — point the fixture at a stub
	// (doctor must measure drain damage, not the environment).
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"model":"gpt-4o-mini","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer stub.Close()
	cfgRaw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	merged := strings.Replace(string(cfgRaw), "provider: gemini\n  api_key: ${GEMINI_API_KEY}", "provider: openai\n  api_key: sk-test\n  base_url: "+stub.URL, 1)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(merged), 0o644); err != nil {
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
	// Budget floor is 10s (clamped) — a never-finishing job consumes the
	// full budget before being cancelled and interrupted (F-040: Stop
	// waits up to the budget, then cancels).
	if elapsed := time.Since(start); elapsed > 11*time.Second {
		t.Errorf("drain took %v, want <= drain-timeout+cushion", elapsed)
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

	// AC-S5's integrity clause: the existing doctor checks pass on the
	// post-drain workspace (not a proxy — the real RunDoctor).
	result := wiki.RunDoctor(dir)
	if result.HasErrors() {
		t.Errorf("doctor found errors on the post-drain workspace:\n%s", wiki.FormatDoctor(result))
	}

	// Ledger integrity: intact and restart-readable.
	l3, err := OpenLedger(dir)
	if err != nil {
		t.Fatalf("ledger unreadable post-drain: %v", err)
	}
	if _, ok := l3.Get(j.ID); !ok {
		t.Error("job lost from ledger after restart")
	}
}

// TestQueueStopGracefulFinish: a job that finishes within the budget
// completes as done (the "finish current job" half of Stop's contract).
func TestQueueStopGracefulFinish(t *testing.T) {
	dir := t.TempDir()
	l, _ := OpenLedger(dir)
	release := make(chan struct{})
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{}`), nil
	}
	q := NewQueue(l, 1, exec, clockSeq(time.Millisecond))
	ctx := context.Background()
	go q.Run(ctx)
	j, _ := q.Submit(CompileJobRequest{})
	time.Sleep(50 * time.Millisecond)
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	got, _ := l.Get(j.ID)
	if got.Status != JobDone {
		t.Errorf("status = %q, want done (graceful finish within budget)", got.Status)
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
