package serve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/metrics"
)

var baseTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func clockSeq(step time.Duration) func() time.Time {
	var mu sync.Mutex
	t := baseTime
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(step)
		return t
	}
}

func TestLedgerRoundTripAndRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	now := clockSeq(time.Millisecond)
	l, err := OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	j1, err := l.Submit(CompileJobRequest{Model: "gpt-4o-mini"}, now)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := l.Submit(CompileJobRequest{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.transition(j1.ID, JobRunning, "", now); err != nil {
		t.Fatal(err)
	}

	// Reload: running → interrupted, pending stays.
	l2, err := OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	got1, ok := l2.Get(j1.ID)
	if !ok {
		t.Fatal("job1 lost")
	}
	if got1.Status != JobInterrupted {
		t.Errorf("running job after restart = %q, want interrupted", got1.Status)
	}
	got2, _ := l2.Get(j2.ID)
	if got2.Status != JobPending {
		t.Errorf("pending job after restart = %q, want pending", got2.Status)
	}
	if ids := l2.PendingIDs(); len(ids) != 1 || ids[0] != j2.ID {
		t.Errorf("PendingIDs = %v", ids)
	}

	// Snapshot safety: mutating a returned job must not affect the store.
	got2.Error = "tampered"
	again, _ := l2.Get(j2.ID)
	if again.Error == "tampered" {
		t.Error("Get returned a live reference, not a snapshot")
	}
}

func TestLedgerBacklog409(t *testing.T) {
	dir := t.TempDir()
	now := clockSeq(time.Nanosecond)
	l, _ := OpenLedger(dir)
	for i := 0; i < maxBacklog; i++ {
		if _, err := l.Submit(CompileJobRequest{}, now); err != nil {
			t.Fatal(err)
		}
	}
	_, err := l.Submit(CompileJobRequest{}, now)
	if !errors.Is(err, ErrBacklog) {
		t.Errorf("err = %v, want ErrBacklog (409)", err)
	}
}

func TestQueueSerializesPerWorkspace(t *testing.T) {
	dir := t.TempDir()
	now := clockSeq(50 * time.Millisecond)
	l, _ := OpenLedger(dir)

	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		mu.Lock()
		order = append(order, "start:"+j.ID)
		mu.Unlock()
		<-release
		mu.Lock()
		order = append(order, "end:"+j.ID)
		mu.Unlock()
		return json.RawMessage(`{"added":1}`), nil
	}
	q := NewQueue(l, 2, exec, now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)

	j1, _ := q.Submit(CompileJobRequest{})
	j2, _ := q.Submit(CompileJobRequest{})

	// Let job1 start, then confirm job2 hasn't started (FIFO serialization).
	time.Sleep(100 * time.Millisecond)
	s1, _ := l.Get(j1.ID)
	s2, _ := l.Get(j2.ID)
	if s1.StartedAt == "" {
		t.Fatal("job1 should be running")
	}
	if s2.StartedAt != "" {
		t.Fatalf("job2 started before job1 finished: serialization broken (%s >= %s)", s2.StartedAt, s1.StartedAt)
	}
	close(release) // j1 finishes; the queue then processes j2 naturally.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, _ := l.Get(j2.ID)
		if s.Status == JobDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("j2 never completed (status %q)", s.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := q.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	s1, _ = l.Get(j1.ID)
	s2, _ = l.Get(j2.ID)
	if s1.Status != JobDone || s2.Status != JobDone {
		t.Fatalf("statuses: %q %q", s1.Status, s2.Status)
	}
	// AC-S4: job2 started no earlier than job1 finished — compare PARSED
	// times: RFC3339Nano trims fractional trailing zeros, so raw string
	// comparison orders ".35Z" before ".3Z" incorrectly.
	t2s, err := time.Parse(time.RFC3339Nano, s2.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	t1f, err := time.Parse(time.RFC3339Nano, s1.FinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if t2s.Before(t1f) {
		t.Errorf("AC-S4 violated: StartedAt[job2]=%s < FinishedAt[job1]=%s", s2.StartedAt, s1.FinishedAt)
	}
	if order[0] != "start:"+j1.ID || order[1] != "end:"+j1.ID || order[2] != "start:"+j2.ID {
		t.Errorf("execution interleaved: %v", order)
	}
}

func TestQueueStopMarksInterrupted(t *testing.T) {
	dir := t.TempDir()
	l, _ := OpenLedger(dir)
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	q := NewQueue(l, 1, exec, clockSeq(time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	j, _ := q.Submit(CompileJobRequest{})
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := q.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := l.Get(j.ID)
	if s.Status != JobInterrupted {
		t.Errorf("status = %q, want interrupted after stop", s.Status)
	}
}

func TestJobTimestampsNano(t *testing.T) {
	dir := t.TempDir()
	l, _ := OpenLedger(dir)
	j, _ := l.Submit(CompileJobRequest{}, clockSeq(time.Nanosecond))
	if !strings.Contains(j.CreatedAt, ".") || len(j.CreatedAt) < 30 {
		t.Errorf("CreatedAt not RFC3339Nano: %q", j.CreatedAt)
	}
}

// TestStopLeavesBacklogPending (N-02): Stop during an active drain does
// NOT start remaining backlog jobs — they stay pending for the restart.
func TestStopLeavesBacklogPending(t *testing.T) {
	dir := t.TempDir()
	l, _ := OpenLedger(dir)
	release := make(chan struct{})
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{}`), nil
	}
	q := NewQueue(l, 1, exec, clockSeq(time.Millisecond))
	go q.Run(context.Background())

	j1, _ := q.Submit(CompileJobRequest{})
	j2, _ := q.Submit(CompileJobRequest{})
	time.Sleep(100 * time.Millisecond) // j1 running, j2 pending

	go func() { close(release) }() // j1 completes shortly
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	s1, _ := l.Get(j1.ID)
	s2, _ := l.Get(j2.ID)
	if s1.Status != JobDone {
		t.Errorf("j1 = %q, want done (current job finishes)", s1.Status)
	}
	if s2.Status != JobPending {
		t.Errorf("j2 = %q, want pending (backlog must NOT start during shutdown)", s2.Status)
	}
}

// TestQueueDepthGaugeMultiQueue (verification review F-002): every stack's
// queue shares one process-global gauge — recovery accounting must be
// additive, or the last queue to start overwrites the others' depth.
func TestQueueDepthGaugeMultiQueue(t *testing.T) {
	metrics.ResetForTest()
	now := clockSeq(50 * time.Millisecond)

	block := make(chan struct{})
	started := make(chan struct{}, 4)
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		started <- struct{}{}
		<-block
		return json.RawMessage(`{}`), nil
	}

	dirA, dirB := t.TempDir(), t.TempDir()
	lA, _ := OpenLedger(dirA)
	lB, _ := OpenLedger(dirB)
	// Pending jobs recovered by the ledgers BEFORE either queue starts.
	for i := 0; i < 2; i++ {
		if _, err := lA.Submit(CompileJobRequest{}, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := lB.Submit(CompileJobRequest{}, now); err != nil {
			t.Fatal(err)
		}
	}

	qA := NewQueue(lA, 1, exec, now)
	qB := NewQueue(lB, 1, exec, now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go qA.Run(ctx)
	go qB.Run(ctx)

	// One job per queue goes in-flight (each Dec's the gauge once).
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("queues did not start their recovered jobs")
		}
	}

	got := int64(-1)
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == "job_queue_depth" {
			got = snap[i+1].(int64)
		}
	}
	// 5 recovered − 2 in-flight = 3 still queued across BOTH queues.
	if got != 3 {
		t.Errorf("job_queue_depth = %d, want 3 (additive across queues)", got)
	}

	// Teardown: release the in-flight jobs and stop both queues BEFORE
	// TempDir cleanup (running queues hold ledger files).
	close(block)
	cancel()
	_ = qA.Stop(context.Background())
	_ = qB.Stop(context.Background())
}

// TestQueueDepthGaugeEvictionReassembly (verification pass 3 F-021): an
// eviction stops a queue leaving its backlog pending; re-assembly builds a
// new Queue over the same ledger — recovery accounting must NOT re-Add the
// jobs the first queue already Inc'd, and the gauge returns to 0 once the
// carried backlog drains.
func TestQueueDepthGaugeEvictionReassembly(t *testing.T) {
	metrics.ResetForTest()
	queueDepthAccounted.Lock()
	queueDepthAccounted.m = map[string]int64{}
	queueDepthAccounted.Unlock()
	now := clockSeq(50 * time.Millisecond)

	gaugeValue := func() int64 {
		snap := metrics.Snapshot()
		for i := 0; i+1 < len(snap); i += 2 {
			if snap[i] == "job_queue_depth" {
				return snap[i+1].(int64)
			}
		}
		return -1
	}

	// exec blocks until its channel closes OR the ctx is cancelled (so
	// Stop can interrupt the in-flight job and leave the backlog pending).
	block1 := make(chan struct{})
	started := make(chan struct{}, 4)
	exec := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		started <- struct{}{}
		select {
		case <-block1:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(`{}`), nil
	}

	dir := t.TempDir()
	l, _ := OpenLedger(dir)
	for i := 0; i < 2; i++ {
		if _, err := l.Submit(CompileJobRequest{}, now); err != nil {
			t.Fatal(err)
		}
	}
	// First queue: its recovery accounting Adds the two ledger-pending
	// jobs; one goes in-flight (Dec → 1), the other stays pending. Stop
	// interrupts the in-flight job; the backlog persists.
	q1 := NewQueue(l, 1, exec, now)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go q1.Run(ctx1)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first queue did not start its job")
	}
	if got := gaugeValue(); got != 1 {
		t.Fatalf("gauge with one in-flight + one pending = %d, want 1", got)
	}
	// Stop FIRST (not ctx cancel): Stop cancels the run ctx, the in-flight
	// exec returns via its ctx watch, and the Run loop exits on the
	// cancelled ctx WITHOUT starting the pending job — the backlog survives.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = q1.Stop(stopCtx)
	stopCancel()
	close(block1)
	if got := gaugeValue(); got != 1 {
		t.Fatalf("gauge after eviction = %d, want 1 (in-flight Dec'd, backlog still counted)", got)
	}

	// Re-assembly: a NEW queue over the SAME ledger runs the recovery
	// accounting — it must not re-Add the carried backlog.
	block2 := make(chan struct{})
	exec2 := func(ctx context.Context, j *Job) (json.RawMessage, error) {
		select {
		case <-block2:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.RawMessage(`{}`), nil
	}
	q2 := NewQueue(l, 2, exec2, now)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go q2.Run(ctx2)
	// q2 claims the carried job (Dec → 0) and blocks in exec2. WITH a
	// double-count, recovery would have re-Added the carried backlog and
	// the gauge would read 1 here; without one it reads exactly 0.
	time.Sleep(100 * time.Millisecond)
	if got := gaugeValue(); got != 0 {
		t.Errorf("gauge after re-assembly = %d, want 0 (carried backlog double-counted)", got)
	}

	// Release the in-flight job — the gauge stays 0.
	close(block2)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if gaugeValue() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := gaugeValue(); got != 0 {
		t.Errorf("gauge after draining the carried backlog = %d, want 0", got)
	}
	cancel2()
	stopCtx2, stopCancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	_ = q2.Stop(stopCtx2)
	stopCancel2()
}

// TestValidServeTier (SPEC-07 D3 cardinality): a client-requested compile
// tier must be in the bounded set {0..3} so the compiles_total tier label
// cannot explode. nil (use-config-default) is always valid.
func TestValidServeTier(t *testing.T) {
	intp := func(n int) *int { return &n }
	for _, c := range []struct {
		name string
		tier *int
		want bool
	}{
		{"nil (config default)", nil, true},
		{"tier 0", intp(0), true},
		{"tier 3", intp(3), true},
		{"tier -1 (rejected: nil means default)", intp(-1), false},
		{"tier 4", intp(4), false},
		{"tier 99", intp(99), false},
	} {
		if got := validServeTier(c.tier); got != c.want {
			t.Errorf("%s: validServeTier=%v, want %v", c.name, got, c.want)
		}
	}
}
