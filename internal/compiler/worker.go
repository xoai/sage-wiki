package compiler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
)

// workerSettings is WorkerConfig with defaults resolved (spec C5:
// 5s/120s/30s/5/16). The heartbeat << lease TTL invariant is enforced by
// config.Validate.
type workerSettings struct {
	PollInterval      time.Duration
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	MaxAttempts       int
	ClaimLimit        int
}

func resolveWorkerConfig(wc config.WorkerConfig) workerSettings {
	s := workerSettings{
		PollInterval:      5 * time.Second,
		LeaseTTL:          120 * time.Second,
		HeartbeatInterval: 30 * time.Second,
		MaxAttempts:       5,
		ClaimLimit:        16,
	}
	if wc.PollIntervalSeconds > 0 {
		s.PollInterval = time.Duration(wc.PollIntervalSeconds) * time.Second
	}
	if wc.LeaseTTLSeconds > 0 {
		s.LeaseTTL = time.Duration(wc.LeaseTTLSeconds) * time.Second
	}
	if wc.HeartbeatIntervalSeconds > 0 {
		s.HeartbeatInterval = time.Duration(wc.HeartbeatIntervalSeconds) * time.Second
	}
	if wc.MaxAttempts > 0 {
		s.MaxAttempts = wc.MaxAttempts
	}
	if wc.ClaimLimit > 0 {
		s.ClaimLimit = wc.ClaimLimit
	}
	return s
}

// WorkerDeps are the worker's construction inputs (spec C3/C4).
type WorkerDeps struct {
	ProjectDir string
	Items      *CompileItemStore
	Coord      *CompileCoordinator
	Progress   *Progress
	Config     workerSettings
	// Process is the claim→process→release body, injectable for tests.
	// nil means "not yet wired" — cycles acquire the coordinator and do
	// nothing (T6 installs the real one).
	Process func(ctx context.Context) (worked bool, err error)
}

// Worker drains the durable compile queue inside serve mode (P2-3).
// One instance per serve process; lease ownership is fenced by token.
type Worker struct {
	deps  WorkerDeps
	token string
}

// NewWorker constructs a Worker with a unique lease-owner token
// (pid-counter, the manifest-lock pattern).
var workerCounter uint64

func NewWorker(deps WorkerDeps) Worker {
	workerCounter++
	return Worker{
		deps:  deps,
		token: fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), workerCounter),
	}
}

// Run drives the claim/process loop until ctx is cancelled. The expired-
// lease sweep runs once at startup (crash recovery) and again per cycle.
func (w *Worker) Run(ctx context.Context) error {
	w.requeueExpired()
	for {
		if _, err := w.cycle(ctx); err != nil {
			log.Error("worker cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.deps.Config.PollInterval):
		}
	}
}

// cycle is one pass of the worker loop (spec C3 steps 1-9). The whole
// cycle runs inside TryCompile's fn — the coordinator release IS the fn
// return — so a lost race skips the cycle with nothing claimed.
func (w *Worker) cycle(ctx context.Context) (bool, error) {
	w.requeueExpired()

	// Batch owns the pipeline until its checkpoint retires (watch-mode
	// guard parity, spec C3 step 2).
	if hasPendingBatch(w.deps.ProjectDir) {
		return false, nil
	}

	worked := false
	ok, err := w.deps.Coord.TryCompile(func() error {
		if w.deps.Process == nil {
			return nil
		}
		didWork, err := w.deps.Process(ctx)
		worked = didWork
		return err
	})
	if err != nil {
		return false, err
	}
	if !ok {
		// An on-demand or MCP compile holds the semaphore — nothing was
		// claimed, so there is nothing to undo. Next poll tries again.
		return false, nil
	}
	return worked, nil
}

func (w *Worker) requeueExpired() {
	n, err := w.deps.Items.RequeueExpired(time.Now().UTC())
	if err != nil {
		log.Error("worker requeue sweep failed", "error", err)
		return
	}
	if n > 0 {
		log.Info("worker requeued expired leases", "count", n)
	}
}
