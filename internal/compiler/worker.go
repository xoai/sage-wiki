package compiler

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
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

// ResolveWorkerConfig applies the documented defaults to a raw
// serve.worker config block (exported for serve wiring, T8).
func ResolveWorkerConfig(wc config.WorkerConfig) workerSettings {
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
	Items      store.CompileItemStore
	// Backend, when set, is injected into each cycle's store stack (the
	// worker shares the caller's handle instead of opening its own DB —
	// spec C4). Must be consistent with Items.
	Backend  store.Backend
	Coord    *CompileCoordinator
	Progress *Progress
	Config   workerSettings
	// Process is the claim→process→release body, injectable for tests.
	// nil installs the production processCycle.
	Process func(ctx context.Context) (worked bool, err error)
}

// NewWorkerForServe builds a Worker for serve-mode wiring (T8): resolves
// the raw config block, pulls the queue store from the shared backend.
func NewWorkerForServe(projectDir string, backend store.Backend, coord *CompileCoordinator, progress *Progress, wc config.WorkerConfig) *Worker {
	return NewWorker(WorkerDeps{
		ProjectDir: projectDir,
		Items:      backend.CompileItems(),
		Backend:    backend,
		Coord:      coord,
		Progress:   progress,
		Config:     ResolveWorkerConfig(wc),
	})
}

// Worker drains the durable compile queue inside serve mode (P2-3).
// One instance per serve process; lease ownership is fenced by token.
type Worker struct {
	deps       WorkerDeps
	token      string
	hooks      passHooks
	failStreak int // consecutive all-failed cycles (hibernation backoff)

	// SPEC-07: the workspace event sink, installed post-construction by
	// the serve wiring (Deps.SetEventSink) — the worker is built in
	// AssembleDeps, before the bus exists.
	sinkMu sync.Mutex
	sink   events.Sink
}

// SetEventSink installs the workspace event sink for worker cycles.
func (w *Worker) SetEventSink(s events.Sink) {
	w.sinkMu.Lock()
	defer w.sinkMu.Unlock()
	w.sink = events.NilSafe(s) // typed-nil guard — see events.NilSafe
}

func (w *Worker) eventSink() events.Sink {
	w.sinkMu.Lock()
	defer w.sinkMu.Unlock()
	return w.sink
}

// NewWorker constructs a Worker with a unique lease-owner token
// (pid-counter, the manifest-lock pattern).
var workerCounter atomic.Uint64

func NewWorker(deps WorkerDeps) *Worker {
	return &Worker{
		deps:  deps,
		token: fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), workerCounter.Add(1)),
		hooks: defaultPassHooks(),
	}
}

// Run drives the claim/process loop until ctx is cancelled. The expired-
// lease sweep runs once at startup (crash recovery) and again per cycle.
// Systemic failures hibernate the worker with exponential backoff.
func (w *Worker) Run(ctx context.Context) error {
	w.requeueExpired()
	for {
		worked, err := w.cycle(ctx)
		if err != nil {
			log.Error("worker cycle failed", "error", err)
		}
		if worked {
			continue // drain: loop immediately while items flow (spec C3 step 9)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(w.cycleSleep()):
		}
	}
}

// cycleSleep backs off exponentially while every claimed item keeps
// failing (a dead LLM/embedder backend must not be hammered), capped at
// 30 minutes; any successful cycle resets to the plain poll interval.
func (w *Worker) cycleSleep() time.Duration {
	d := w.deps.Config.PollInterval
	for i := 0; i < w.failStreak; i++ {
		d *= 2
	}
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
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

	process := w.deps.Process
	if process == nil {
		process = w.processCycle
	}
	worked := false
	ok, err := w.deps.Coord.TryCompile(func() error {
		didWork, err := process(ctx)
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
		w.deps.Progress.QueueEvent("requeued", "expired leases", n)
	}
}
