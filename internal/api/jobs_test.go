package api

import (
	"strings"
	"testing"
	"time"
)

func TestJobStore_Create(t *testing.T) {
	s := newJobStore()
	fixedTime := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixedTime }

	j := s.create(JobCompile)
	if j.ID == "" {
		t.Fatal("expected non-empty job ID")
	}
	if j.Kind != JobCompile {
		t.Fatalf("expected kind=compile, got %s", j.Kind)
	}
	if j.Status != JobPending {
		t.Fatalf("expected status=pending, got %s", j.Status)
	}
	if !j.SubmittedAt.Equal(fixedTime) {
		t.Fatalf("expected submitted_at=%v, got %v", fixedTime, j.SubmittedAt)
	}
	if j.StartedAt != nil {
		t.Fatal("expected started_at=nil")
	}
	if j.FinishedAt != nil {
		t.Fatal("expected finished_at=nil")
	}
}

func TestJobStore_Get(t *testing.T) {
	s := newJobStore()
	j := s.create(JobCompile)

	got, ok := s.get(j.ID)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if got.ID != j.ID {
		t.Fatalf("expected id=%s, got %s", j.ID, got.ID)
	}

	_, ok = s.get("nonexistent")
	if ok {
		t.Fatal("expected no job for nonexistent id")
	}
}

func TestJobStore_List(t *testing.T) {
	s := newJobStore()

	// Empty list.
	all := s.list("")
	if len(all) != 0 {
		t.Fatalf("expected empty list, got %d", len(all))
	}

	j1 := s.create(JobCompile)
	_ = s.create(JobLint)
	j3 := s.create(JobCompileTopic)

	// List all — most recent first.
	all = s.list("")
	if len(all) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(all))
	}
	if all[0].ID != j3.ID {
		t.Fatalf("expected most recent %s first, got %s", j3.ID, all[0].ID)
	}
	if all[2].ID != j1.ID {
		t.Fatalf("expected oldest %s last, got %s", j1.ID, all[2].ID)
	}

	// Status filter — all pending.
	pending := s.list(JobPending)
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	// Status filter — no running.
	running := s.list(JobRunning)
	if len(running) != 0 {
		t.Fatalf("expected 0 running, got %d", len(running))
	}
}

func TestJobStore_FIFOEviction(t *testing.T) {
	s := newJobStore()

	var firstID string
	for i := 0; i < 110; i++ {
		j := s.create(JobCompile)
		if i == 0 {
			firstID = j.ID
		}
	}

	all := s.list("")
	if len(all) != jobStoreMax {
		t.Fatalf("expected %d jobs after eviction, got %d", jobStoreMax, len(all))
	}

	// The first created job should be evicted.
	_, ok := s.get(firstID)
	if ok {
		t.Fatal("expected oldest job to be evicted")
	}
}

func TestJobStore_ActiveJobOfKind(t *testing.T) {
	s := newJobStore()

	// No active.
	_, active := s.activeJobOfKind(JobCompile)
	if active {
		t.Fatal("expected no active compile job")
	}

	j1 := s.create(JobCompile)

	// Active (pending).
	j, active := s.activeJobOfKind(JobCompile)
	if !active {
		t.Fatal("expected active compile job")
	}
	if j.ID != j1.ID {
		t.Fatalf("expected job %s, got %s", j1.ID, j.ID)
	}

	// Mark as done — no longer active.
	s.mu.Lock()
	j1.Status = JobDone
	s.mu.Unlock()

	_, active = s.activeJobOfKind(JobCompile)
	if active {
		t.Fatal("expected no active compile after done")
	}

	// Lint should not conflict with compile.
	s.create(JobLint)
	_, active = s.activeJobOfKind(JobCompile)
	if active {
		t.Fatal("expected no active compile (lint is different kind)")
	}
	_, active = s.activeJobOfKind(JobLint)
	if !active {
		t.Fatal("expected active lint job")
	}
}

func TestJobStore_IdempotencyKey(t *testing.T) {
	s := newJobStore()

	// Miss.
	_, ok := s.lookupIdemKey(JobCompile, "key-1")
	if ok {
		t.Fatal("expected key miss")
	}

	// Store + hit.
	s.storeIdemKey(JobCompile, "key-1", "job-1")
	id, ok := s.lookupIdemKey(JobCompile, "key-1")
	if !ok {
		t.Fatal("expected key hit after store")
	}
	if id != "job-1" {
		t.Fatalf("expected job-1, got %s", id)
	}

	// Per-kind scoping.
	s.storeIdemKey(JobLint, "key-1", "job-2")
	id, ok = s.lookupIdemKey(JobLint, "key-1")
	if !ok {
		t.Fatal("expected lint key hit")
	}
	if id != "job-2" {
		t.Fatalf("expected job-2, got %s", id)
	}
	id, ok = s.lookupIdemKey(JobCompile, "key-1")
	if !ok {
		t.Fatal("expected compile key still present")
	}
	if id != "job-1" {
		t.Fatalf("expected job-1 for compile, got %s", id)
	}
}

func TestJobStore_ConcurrentSafety(t *testing.T) {
	s := newJobStore()
	done := make(chan struct{})

	// Concurrent readers + writers.
	go func() {
		for i := 0; i < 100; i++ {
			s.create(JobCompile)
			s.list("")
			s.activeJobOfKind(JobCompile)
			s.storeIdemKey(JobCompile, "k", "v")
			s.lookupIdemKey(JobCompile, "k")
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		s.create(JobLint)
		s.list(JobRunning)
		s.activeJobOfKind(JobLint)
	}

	<-done
}

func TestJobStore_ListStatusFilter(t *testing.T) {
	s := newJobStore()

	j := s.create(JobCompile)

	// Change status to running.
	s.mu.Lock()
	j.Status = JobRunning
	now := time.Now()
	j.StartedAt = &now
	s.mu.Unlock()

	// Filter by running.
	running := s.list(JobRunning)
	if len(running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(running))
	}

	// Filter by done — none.
	done := s.list(JobDone)
	if len(done) != 0 {
		t.Fatalf("expected 0 done, got %d", len(done))
	}

	// Filter by invalid status returns empty.
	invalid := s.list(JobStatus("bogus"))
	if len(invalid) != 0 {
		t.Fatalf("expected 0 for bogus status, got %d", len(invalid))
	}
}

func TestNewJobID_Uniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := newJobID()
		if seen[id] {
			t.Fatalf("duplicate job ID: %s", id)
		}
		seen[id] = true
		if len(id) == 0 {
			t.Fatal("expected non-empty ID")
		}
	}
}

func TestParseJobListRequest(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		wantOK  bool
		wantErr string
	}{
		{"topic mode", []byte(`{"topic":"quantum"}`), true, ""},
		{"topic with max_sources", []byte(`{"topic":"q","max_sources":5}`), true, ""},
		{"full compile dry_run", []byte(`{"dry_run":true}`), true, ""},
		{"full compile explicit false flag (presence selects mode)", []byte(`{"dry_run":false}`), true, ""},
		{"full compile fresh", []byte(`{"fresh":true}`), true, ""},
		{"full compile prune", []byte(`{"prune":true}`), true, ""},
		{"full compile multiple flags", []byte(`{"dry_run":true,"fresh":true}`), true, ""},
		{"both topic and flags", []byte(`{"topic":"q","dry_run":true}`), false, "exactly one of 'topic' or compile flags expected"},
		{"neither topic nor flags", []byte(`{}`), false, "either 'topic' or compile flags required"},
		{"empty body", []byte(``), false, "either 'topic' or compile flags required"},
		{"invalid json", []byte(`{not json`), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseJobListRequest(tt.body)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
			}
		})
	}
}

func TestJob_ActiveAndFinished(t *testing.T) {
	j := &Job{Status: JobPending}
	if !j.Active() {
		t.Fatal("pending should be active")
	}
	if j.Finished() {
		t.Fatal("pending should not be finished")
	}

	j.Status = JobRunning
	if !j.Active() {
		t.Fatal("running should be active")
	}
	if j.Finished() {
		t.Fatal("running should not be finished")
	}

	j.Status = JobDone
	if j.Active() {
		t.Fatal("done should not be active")
	}
	if !j.Finished() {
		t.Fatal("done should be finished")
	}

	j.Status = JobFailed
	if j.Active() {
		t.Fatal("failed should not be active")
	}
	if !j.Finished() {
		t.Fatal("failed should be finished")
	}

	j.Status = JobCancelled
	if j.Active() {
		t.Fatal("cancelled should not be active")
	}
	if !j.Finished() {
		t.Fatal("cancelled should be finished")
	}
}

func TestJobToResponse(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	j := &Job{
		ID:          "test-id",
		Kind:        JobCompile,
		Status:      JobDone,
		SubmittedAt: now,
		StartedAt:   &now,
		FinishedAt:  &now,
		Result:      map[string]any{"compiled": 5},
	}

	resp := jobToResponse(j)
	if resp.JobID != "test-id" {
		t.Fatalf("expected job_id=test-id, got %s", resp.JobID)
	}
	if resp.Kind != JobCompile {
		t.Fatalf("expected kind=compile, got %s", resp.Kind)
	}
	if resp.Status != JobDone {
		t.Fatalf("expected status=done, got %s", resp.Status)
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}
	if resp.Error != nil {
		t.Fatal("expected nil error")
	}
}
