package serve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
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
	close(release)
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
	block := make(chan struct{})
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
	close(block)
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
