// Package serve implements the SPEC-02 long-lived server: REST over the
// engine surface, MCP over streamable HTTP, persistent async compile
// jobs, token-file auth, rate-limit hooks, and graceful shutdown.
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// Job status vocabulary — SHARED with internal/api's job store
// (divergence guard, spec §2.3): pending|running|done|failed|cancelled.
// interrupted is the ledger's only addition: the terminal state assigned
// on restart to jobs that were running when the process died. It maps to
// failed on any future /v1 read (SPEC-07 consolidates).
const (
	JobPending     = "pending"
	JobRunning     = "running"
	JobDone        = "done"
	JobFailed      = "failed"
	JobCancelled   = "cancelled"
	JobInterrupted = "interrupted"
)

// CompileJobRequest is the POST /compile body (parameters only — never
// source content, spec §2.3).
type CompileJobRequest struct {
	Tier    *int    `json:"tier,omitempty"`
	Model   string  `json:"model,omitempty"`
	MaxDocs int     `json:"max_docs,omitempty"`
	MaxCost *string `json:"max_cost,omitempty"` // decimal string; nil = no guard
}

// Job is one async compile job.
type Job struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"` // "compile"
	Status     string            `json:"status"`
	Request    CompileJobRequest `json:"request"`
	Result     json.RawMessage   `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  string            `json:"created_at"`  // RFC3339Nano
	StartedAt  string            `json:"started_at"`  // RFC3339Nano; empty until running
	FinishedAt string            `json:"finished_at"` // RFC3339Nano; empty until terminal
}

// ledgerEvent is one line of .sage/jobs.jsonl.
type ledgerEvent struct {
	Op    string `json:"op"` // submit|start|finish
	Job   *Job   `json:"job"`
	Error string `json:"error,omitempty"`
}

const maxBacklog = 100

// ErrBacklog is the 409 conflict for a full ledger.
var ErrBacklog = fmt.Errorf("job backlog full (max %d)", maxBacklog)

// Ledger is the restart-proof job record (.sage/jobs.jsonl). One writer
// process at a time (the workspace lock guarantees it).
type Ledger struct {
	path    string
	mu      sync.Mutex
	jobs    map[string]*Job
	counter uint64 // collision-proof ID suffix (coarse clocks, F-038)
}

// queueDepthAccounted tracks, per ledger path, the job_queue_depth gauge
// contribution this process has already made. Recovery accounting (Queue.Run)
// is additive yet non-duplicative across eviction→reassembly cycles: a new
// Queue over the same ledger re-Adds only jobs no earlier queue Inc'd
// (verification pass 3).
var queueDepthAccounted = struct {
	sync.Mutex
	m map[string]int64
}{m: map[string]int64{}}

// All gauge accounting happens UNDER the owning ledger's mutex so a
// concurrent Submit and the recovery snapshot cannot double-count a job
// (verification pass 5): insert+Inc and the pending snapshot are then
// atomic with respect to each other.
func queueDepthIncLocked(path string) {
	queueDepthAccounted.Lock()
	queueDepthAccounted.m[path]++
	queueDepthAccounted.Unlock()
	metrics.GaugeNamed("job_queue_depth").Inc()
}

func queueDepthDecLocked(path string) {
	queueDepthAccounted.Lock()
	queueDepthAccounted.m[path]--
	queueDepthAccounted.Unlock()
	metrics.GaugeNamed("job_queue_depth").Dec()
}

// queueDepthRecover snapshots pending under the ledger mutex, then applies
// the delta — atomic against concurrent Submit insert+Inc.
func queueDepthRecover(l *Ledger) {
	l.mu.Lock()
	pending := int64(0)
	for _, j := range l.jobs {
		if j.Status == JobPending {
			pending++
		}
	}
	queueDepthAccounted.Lock()
	delta := pending - queueDepthAccounted.m[l.path]
	queueDepthAccounted.m[l.path] = pending
	queueDepthAccounted.Unlock()
	l.mu.Unlock()
	if delta != 0 {
		metrics.GaugeNamed("job_queue_depth").Add(delta)
	}
}

// OpenLedger loads (or creates) the ledger, applying restart recovery:
// running → interrupted, pending stays pending for re-enqueue.
func OpenLedger(wsDir string) (*Ledger, error) {
	l := &Ledger{path: filepath.Join(wsDir, ".sage", "jobs.jsonl"), jobs: map[string]*Job{}}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("read job ledger: %w", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev ledgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("job ledger line %d malformed: %w", i+1, err)
		}
		l.jobs[ev.Job.ID] = ev.Job
	}
	// Restart recovery: a job left running when the process died is
	// marked interrupted — never silently "running" forever. The
	// transition IS appended (F-042): the file is the restart-proof
	// record, and it must not claim a job is still running.
	now := time.Now
	var ids []string
	for id := range l.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids) // rule 6: ledger line order must not inherit map order (Q-4)
	for _, id := range ids {
		j := l.jobs[id]
		if j.Status == JobRunning {
			j.Status = JobInterrupted
			j.FinishedAt = now().UTC().Format(time.RFC3339Nano)
			if err := l.appendLocked(ledgerEvent{Op: "transition", Job: j, Error: "process died mid-job"}); err != nil {
				return nil, err
			}
		}
		if n := parseJobCounter(j.ID); n > l.counter {
			l.counter = n
		}
	}
	return l, nil
}

// parseJobCounter extracts the -N suffix of a job ID.
func parseJobCounter(id string) uint64 {
	i := strings.LastIndex(id, "-")
	if i == -1 {
		return 0
	}
	var n uint64
	fmt.Sscanf(id[i+1:], "%d", &n)
	return n
}

// Submit records a new job (202), or ErrBacklog (409) when full.
func (l *Ledger) Submit(req CompileJobRequest, now func() time.Time) (*Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	backlog := 0
	for _, j := range l.jobs {
		if j.Status == JobPending || j.Status == JobRunning {
			backlog++
		}
	}
	if backlog >= maxBacklog {
		return nil, ErrBacklog
	}
	l.counter++
	j := &Job{
		ID:        fmt.Sprintf("job-%d-%d", now().UnixNano(), l.counter),
		Kind:      "compile",
		Status:    JobPending,
		Request:   req,
		CreatedAt: now().UTC().Format(time.RFC3339Nano),
	}
	// Append BEFORE the in-memory insert (verification pass 4): if the
	// append fails, the job must not linger pending in memory — the
	// queue-depth gauge Inc happens in Queue.Submit after a successful
	// ledger submit, and a lingering pending job would later Dec without
	// a matching Inc.
	if err := l.appendLocked(ledgerEvent{Op: "submit", Job: j}); err != nil {
		return nil, err
	}
	l.jobs[j.ID] = j
	queueDepthIncLocked(l.path) // under l.mu — atomic vs the recover snapshot
	return j, nil
}

// transition moves a job to a new status, stamping the time.
func (l *Ledger) transition(id, status, errText string, now func() time.Time) (*Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	j.Status = status
	if status == JobRunning {
		j.StartedAt = now().UTC().Format(time.RFC3339Nano)
	}
	if isTerminal(status) {
		j.FinishedAt = now().UTC().Format(time.RFC3339Nano)
	}
	if errText != "" {
		j.Error = errText
	}
	return j, l.appendLocked(ledgerEvent{Op: "transition", Job: j, Error: errText})
}

func isTerminal(status string) bool {
	return status == JobDone || status == JobFailed || status == JobCancelled || status == JobInterrupted
}

// Get returns a SNAPSHOT of the job (mutation-safe reads; the
// p4-distribution read-after-unlock lesson).
func (l *Ledger) Get(id string) (*Job, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

// List returns snapshots of all jobs, newest first.
func (l *Ledger) List(limit int) []*Job {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*Job, 0, len(l.jobs))
	for _, j := range l.jobs {
		cp := *j
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].CreatedAt != out[k].CreatedAt {
			return out[i].CreatedAt > out[k].CreatedAt
		}
		return out[i].ID < out[k].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PendingIDs returns pending job IDs in submission order (restart resume).
func (l *Ledger) PendingIDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*Job
	for _, j := range l.jobs {
		if j.Status == JobPending {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].CreatedAt != out[k].CreatedAt {
			return out[i].CreatedAt < out[k].CreatedAt
		}
		return out[i].ID < out[k].ID
	})
	ids := make([]string, 0, len(out))
	for _, j := range out {
		ids = append(ids, j.ID)
	}
	return ids
}

// appendLocked writes one line to the ledger (caller holds mu).
func (l *Ledger) appendLocked(ev ledgerEvent) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open job ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("append job ledger: %w", err)
	}
	return f.Sync()
}

// Queue is a FIFO-per-workspace job executor with a global concurrency
// cap (SPEC-02: serialize per workspace; --max-concurrent-compiles
// matters across workspaces in SPEC-06's shape).
type Queue struct {
	ledger *Ledger
	exec   func(ctx context.Context, j *Job) (json.RawMessage, error)
	now    func() time.Time
	sem    chan struct{}
	wake   chan struct{}
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
	mu     sync.Mutex // guards cancel
	cancel context.CancelFunc
}

// NewQueue builds a queue over the ledger. exec runs one job (the real
// compile or a test fake); now is the clock seam.
func NewQueue(ledger *Ledger, maxConcurrent int, exec func(ctx context.Context, j *Job) (json.RawMessage, error), now func() time.Time) *Queue {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if now == nil {
		now = time.Now
	}
	return &Queue{
		ledger: ledger,
		exec:   exec,
		now:    now,
		sem:    make(chan struct{}, maxConcurrent),
		wake:   make(chan struct{}, 1),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Submit enqueues and nudges the worker.
func (q *Queue) Submit(req CompileJobRequest) (*Job, error) {
	j, err := q.ledger.Submit(req, q.now)
	if err != nil {
		return nil, err
	}
	// SPEC-07: the gauge Inc happened inside Ledger.Submit under the
	// ledger mutex (atomic with the recovery snapshot — verification
	// pass 5); the paired Dec is in runOne.
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return j, nil
}

// Run processes jobs FIFO until Stop. Restart-pending jobs run first
// (initial drain). Stop means "finish in-flight work up to the budget,
// then halt" — see Stop for the exact contract.
func (q *Queue) Run(ctx context.Context) {
	var runCtx context.Context
	runCtx, cancel := context.WithCancel(ctx)
	q.mu.Lock()
	q.cancel = cancel
	q.mu.Unlock()
	defer close(q.doneCh)
	// SPEC-07: recovery accounting for restart-resumed jobs (recovered by
	// the ledger, not by Submit). Additive yet non-duplicative: the
	// per-ledger accounting map keeps an eviction→reassembly cycle from
	// re-Adding jobs an earlier queue already Inc'd, and keeps
	// multi-workspace queues from overwriting each other (a Set would).
	queueDepthRecover(q.ledger)
	q.drainOnce(runCtx) // restart-resumed pending jobs run first (F-033)
	for {
		select {
		case <-runCtx.Done():
			return
		case <-q.stopCh:
			// Finish the CURRENT job only (spec §2.7's "finish current job
			// or mark interrupted") — the backlog stays pending and
			// resumes on the next start. Draining the whole backlog would
			// start fresh compiles during shutdown.
			return
		case <-q.wake:
			q.drainOnce(runCtx)
		}
	}
}

func (q *Queue) drainOnce(ctx context.Context) {
	for {
		ids := q.ledger.PendingIDs()
		if len(ids) == 0 {
			return
		}
		// Deterministic stop check BEFORE starting the next job (N-02):
		// a tied sem/stopCh select picks randomly — never let the backlog
		// start during shutdown.
		select {
		case <-q.stopCh:
			return
		default:
		}
		select {
		case q.sem <- struct{}{}:
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		}
		q.runOne(ctx, ids[0])
		<-q.sem
	}
}

func (q *Queue) runOne(ctx context.Context, id string) {
	defer func() {
		if r := recover(); r != nil {
			q.ledger.transition(id, JobFailed, fmt.Sprintf("panic: %v", r), q.now)
		}
	}()
	j, err := q.ledger.transition(id, JobRunning, "", q.now)
	if err != nil {
		// The start transition mutates the job to running before its
		// append; on append failure the job is no longer pending and
		// never retried in-process. Release the gauge and log — never
		// leak the +1 silently (verification pass 5).
		q.ledger.mu.Lock()
		queueDepthDecLocked(q.ledger.path)
		q.ledger.mu.Unlock()
		log.Error("queue: start transition failed — job stranded, gauge released", "job", id, "error", err)
		return
	}
	q.ledger.mu.Lock()
	queueDepthDecLocked(q.ledger.path)
	q.ledger.mu.Unlock()
	res, err := q.exec(ctx, j)
	if err != nil {
		if ctx.Err() != nil {
			q.ledger.transition(id, JobInterrupted, err.Error(), q.now)
			return
		}
		q.ledger.transition(id, JobFailed, err.Error(), q.now)
		return
	}
	q.ledger.mu.Lock()
	if cur, ok := q.ledger.jobs[id]; ok {
		cur.Result = res
	}
	q.ledger.mu.Unlock()
	q.ledger.transition(id, JobDone, "", q.now)
}

// Stopped reports whether Stop has been signaled — the IsInterrupted seam
// (SPEC-07): a ctx cancellation during a stopped queue maps to Outcome
// "interrupted", not "cancelled".
func (q *Queue) Stopped() bool {
	select {
	case <-q.stopCh:
		return true
	default:
		return false
	}
}

// Stop signals the worker to halt and waits for in-flight work to
// finish UP TO ctx (the drain budget — "finish current job", spec §2.7).
// Only on budget expiry does it cancel in-flight execs (they mark
// interrupted). Safe to call before Run starts.
func (q *Queue) Stop(ctx context.Context) error {
	q.mu.Lock()
	started := q.cancel != nil
	q.mu.Unlock()
	if !started {
		return nil // Run never started — nothing to drain (Q-6)
	}
	q.once.Do(func() { close(q.stopCh) })
	select {
	case <-q.doneCh:
		return nil
	case <-ctx.Done():
	}
	q.mu.Lock()
	cancel := q.cancel
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-q.doneCh:
		return nil
	case <-time.After(2 * time.Second):
		return ctx.Err()
	}
}
