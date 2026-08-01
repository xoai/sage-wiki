//go:build unix

package serve

import (
	"context"
	"encoding/json"
	"syscall"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// TestSIGTERMUnix is AC-S5's OS-guarded e2e form: SIGTERM during a
// blocked compile exits within drain-timeout with the job interrupted
// and the lock released.
func TestSIGTERMUnix(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(deps, nil, Config{Workspace: dir, DrainTimeout: 10 * time.Second, ReadyFn: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	w, err := engine.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetWorkspace(w)

	srv.queue = NewQueue(srv.ledger, 1, func(ctx context.Context, j *Job) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, clockSeq(time.Millisecond))
	j, err := srv.queue.Submit(CompileJobRequest{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := signalNotify()
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, "127.0.0.1:0") }()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("Serve returned: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("SIGTERM did not drain within 15s")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("drain took %v, want < drain-timeout+cushion", elapsed)
	}

	got, _ := srv.ledger.Get(j.ID)
	if got.Status != JobInterrupted {
		t.Errorf("job status = %q, want interrupted", got.Status)
	}
	w2, err := engine.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock not released after SIGTERM drain: %v", err)
	}
	w2.Close()
}
