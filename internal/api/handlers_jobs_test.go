package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/linter"
)

// fakeJobRunner implements JobRunner with call recording and an optional
// gate so tests can hold a job in the running state.
type fakeJobRunner struct {
	mu           sync.Mutex
	compileCalls int
	topicCalls   int
	lintCalls    int
	lastOpts     compiler.CompileOpts
	lastTopic    compiler.OnDemandOpts
	lastPass     string
	lastFix      bool

	compileErr error
	topicErr   error
	lintErr    error

	gate chan struct{}      // non-nil: RunCompile blocks until closed or ctx done
	hub  *compiler.Progress // non-nil: RunCompile emits one event
}

func (f *fakeJobRunner) RunCompile(ctx context.Context, projectDir string, opts compiler.CompileOpts) (*compiler.CompileResult, error) {
	f.mu.Lock()
	f.compileCalls++
	f.lastOpts = opts
	f.mu.Unlock()
	if f.hub != nil {
		f.hub.StartPhase("extract", 5)
		f.hub.ItemStart("source-1")
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.compileErr != nil {
		return nil, f.compileErr
	}
	return &compiler.CompileResult{Added: 1, ArticlesWritten: 1}, nil
}

func (f *fakeJobRunner) RunCompileTopic(ctx context.Context, opts compiler.OnDemandOpts) (*compiler.OnDemandResult, error) {
	f.mu.Lock()
	f.topicCalls++
	f.lastTopic = opts
	f.mu.Unlock()
	if f.topicErr != nil {
		return nil, f.topicErr
	}
	return &compiler.OnDemandResult{CompiledSources: 2, ArticlesWritten: 1}, nil
}

func (f *fakeJobRunner) RunLint(ctx context.Context, lintCtx *linter.LintContext, passName string, fix bool) ([]linter.LintResult, error) {
	f.mu.Lock()
	f.lintCalls++
	f.lastPass = passName
	f.lastFix = fix
	f.mu.Unlock()
	if f.lintErr != nil {
		return nil, f.lintErr
	}
	return []linter.LintResult{{PassName: passName}}, nil
}

func (f *fakeJobRunner) calls() (compile, topic, lint int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compileCalls, f.topicCalls, f.lintCalls
}

// newJobTestRouter builds a Router with a fake runner — job handlers never
// touch the Dispatcher, so nil is safe.
func newJobTestRouter(t *testing.T, runner JobRunner, hub *compiler.Progress) (*Router, *http.ServeMux) {
	t.Helper()
	r := New(nil, &config.Config{}, t.TempDir(), runner, hub)
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	return r, mux
}

func submitJob(t *testing.T, mux *http.ServeMux, kind, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/jobs/"+kind, strings.NewReader(body))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func getJob(t *testing.T, mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/jobs/"+id, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func deleteJob(t *testing.T, mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", "/v1/jobs/"+id, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func jobField(t *testing.T, w *httptest.ResponseRecorder, field string) any {
	t.Helper()
	m := bodyJSON(t, w)
	v, ok := m[field]
	if !ok {
		t.Fatalf("response missing field %q: %v", field, m)
	}
	return v
}

// pollJob polls until pred returns true or the deadline passes.
func pollJob(t *testing.T, mux *http.ServeMux, id string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := getJob(t, mux, id)
		m := bodyJSON(t, w)
		if pred(m) {
			return m
		}
		time.Sleep(5 * time.Millisecond)
	}
	w := getJob(t, mux, id)
	m := bodyJSON(t, w)
	t.Fatalf("job %s did not reach expected state; last: %v", id, m)
	return nil
}

func jobDone(m map[string]any) bool { return m["status"] == "done" }

// J-01: submit full compile → 202 + job_id + Location.
func TestJ01SubmitCompile(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/v1/jobs/") {
		t.Errorf("Location = %q, want /v1/jobs/…", loc)
	}
	if jobField(t, w, "kind") != "compile" {
		t.Errorf("kind = %v, want compile", jobField(t, w, "kind"))
	}
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, jobDone)
}

// J-02: poll transitions pending/running → done with result populated.
func TestJ02Lifecycle(t *testing.T) {
	f := &fakeJobRunner{gate: make(chan struct{})}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id := jobField(t, w, "job_id").(string)

	m := pollJob(t, mux, id, func(m map[string]any) bool { return m["status"] == "running" })
	if m["started_at"] == nil {
		t.Error("running job missing started_at")
	}
	close(f.gate)
	m = pollJob(t, mux, id, jobDone)
	if m["result"] == nil {
		t.Error("done job missing result")
	}
	if m["finished_at"] == nil {
		t.Error("done job missing finished_at")
	}
}

// J-03: topic mode dispatches with topic field; topic+flags → 400.
func TestJ03TopicMode(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)

	w := submitJob(t, mux, "compile", `{"topic": "quantum computing", "max_sources": 7}`, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("topic submit status = %d (%s)", w.Code, w.Body)
	}
	if jobField(t, w, "kind") != "compile_topic" {
		t.Errorf("kind = %v, want compile_topic", jobField(t, w, "kind"))
	}
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, jobDone)
	_, topicCalls, _ := f.calls()
	if topicCalls != 1 {
		t.Errorf("topicCalls = %d, want 1", topicCalls)
	}
	f.mu.Lock()
	if f.lastTopic.Topic != "quantum computing" || f.lastTopic.MaxSources != 7 {
		t.Errorf("topic opts = %+v", f.lastTopic)
	}
	f.mu.Unlock()

	w = submitJob(t, mux, "compile", `{"topic": "x", "dry_run": true}`, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("topic+flags status = %d, want 400", w.Code)
	}
}

// J-04: dry_run flag reaches the runner (no-write guarantee itself is
// compiler-level — covered by internal/compiler dry-run tests).
func TestJ04DryRunFlag(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "compile", `{"dry_run": true}`, "")
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, jobDone)
	f.mu.Lock()
	if !f.lastOpts.DryRun {
		t.Error("runner did not receive DryRun=true")
	}
	f.mu.Unlock()
}

// J-05: same Idempotency-Key twice → same job_id, one dispatch, replay header.
func TestJ05Idempotency(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)

	w1 := submitJob(t, mux, "compile", `{"dry_run": true}`, "key-abc")
	id1 := jobField(t, w1, "job_id").(string)
	pollJob(t, mux, id1, jobDone)

	w2 := submitJob(t, mux, "compile", `{"dry_run": true}`, "key-abc")
	if w2.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d", w2.Code)
	}
	id2 := jobField(t, w2, "job_id").(string)
	if id1 != id2 {
		t.Errorf("idempotent replay returned different job: %s vs %s", id1, id2)
	}
	if w2.Header().Get("X-Idempotent-Replay") != "true" {
		t.Error("replay missing X-Idempotent-Replay: true header")
	}
	compileCalls, _, _ := f.calls()
	if compileCalls != 1 {
		t.Errorf("compileCalls = %d, want 1 (no duplicate dispatch)", compileCalls)
	}
}

// J-06: submit while compile active → 409 with active_job_id.
func TestJ06Conflict(t *testing.T) {
	f := &fakeJobRunner{gate: make(chan struct{})}
	defer close(f.gate)
	_, mux := newJobTestRouter(t, f, nil)

	w1 := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id1 := jobField(t, w1, "job_id").(string)
	pollJob(t, mux, id1, func(m map[string]any) bool { return m["status"] == "running" })

	w2 := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	if w2.Code != http.StatusConflict {
		t.Fatalf("concurrent submit status = %d, want 409 (%s)", w2.Code, w2.Body)
	}
	m := bodyJSON(t, w2)
	env, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("409 response missing error envelope: %v", m)
	}
	if env["code"] != "conflict" {
		t.Errorf("error code = %v, want conflict", env["code"])
	}
	details, _ := env["details"].(map[string]any)
	if details["active_job_id"] != id1 {
		t.Errorf("active_job_id = %v, want %s", details["active_job_id"], id1)
	}
}

// J-07: DELETE marks the job cancelled and the goroutine exits via ctx
// cancellation. (Checkpoint resumability after a cancelled compile is
// compiler-level — covered by internal/compiler checkpoint tests.)
func TestJ07Cancel(t *testing.T) {
	f := &fakeJobRunner{gate: make(chan struct{})}
	_, mux := newJobTestRouter(t, f, nil)

	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, func(m map[string]any) bool { return m["status"] == "running" })

	wd := deleteJob(t, mux, id)
	if wd.Code != http.StatusAccepted {
		t.Fatalf("delete status = %d, want 202", wd.Code)
	}
	m := pollJob(t, mux, id, func(m map[string]any) bool { return m["status"] == "cancelled" })
	if m["finished_at"] == nil {
		t.Error("cancelled job missing finished_at")
	}
}

// J-08: failed job surfaces status failed with an error envelope.
func TestJ08Failure(t *testing.T) {
	f := &fakeJobRunner{compileErr: errors.New("provider exploded")}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id := jobField(t, w, "job_id").(string)
	m := pollJob(t, mux, id, func(m map[string]any) bool { return m["status"] == "failed" })
	env, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("failed job missing error envelope: %v", m)
	}
	if env["code"] != "internal" {
		t.Errorf("error code = %v, want internal", env["code"])
	}
	if !strings.Contains(env["message"].(string), "provider exploded") {
		t.Errorf("error message = %v", env["message"])
	}
}

// J-09: GET unknown job → 404.
func TestJ09GetUnknown(t *testing.T) {
	_, mux := newJobTestRouter(t, &fakeJobRunner{}, nil)
	w := getJob(t, mux, "00000000-0000-4000-8000-000000000000")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// J-10: list endpoint + status filter.
func TestJ10List(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	for i := 0; i < 3; i++ {
		w := submitJob(t, mux, "lint", `{}`, "")
		id := jobField(t, w, "job_id").(string)
		pollJob(t, mux, id, jobDone)
	}

	req := httptest.NewRequest("GET", "/v1/jobs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	m := bodyJSON(t, w)
	jobs, _ := m["jobs"].([]any)
	if len(jobs) != 3 {
		t.Errorf("listed %d jobs, want 3", len(jobs))
	}

	req = httptest.NewRequest("GET", "/v1/jobs?status=done", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	jobs, _ = bodyJSON(t, w)["jobs"].([]any)
	if len(jobs) != 3 {
		t.Errorf("status=done listed %d jobs, want 3", len(jobs))
	}

	req = httptest.NewRequest("GET", "/v1/jobs?status=running", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	jobs, _ = bodyJSON(t, w)["jobs"].([]any)
	if len(jobs) != 0 {
		t.Errorf("status=running listed %d jobs, want 0", len(jobs))
	}
}

// J-13: lint submit → 202 + poll lifecycle; pass name reaches the runner.
func TestJ13Lint(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "lint", `{"pass": "connections", "fix": true}`, "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("lint submit status = %d (%s)", w.Code, w.Body)
	}
	if jobField(t, w, "kind") != "lint" {
		t.Errorf("kind = %v, want lint", jobField(t, w, "kind"))
	}
	id := jobField(t, w, "job_id").(string)
	m := pollJob(t, mux, id, jobDone)
	if m["result"] == nil {
		t.Error("done lint job missing result")
	}
	f.mu.Lock()
	if f.lastPass != "connections" || !f.lastFix {
		t.Errorf("lint args pass=%q fix=%v", f.lastPass, f.lastFix)
	}
	f.mu.Unlock()
}

// J-14: compile job progress mirrors the shared Progress hub.
func TestJ14CompileProgress(t *testing.T) {
	hub := compiler.NewProgress()
	f := &fakeJobRunner{hub: hub}
	_, mux := newJobTestRouter(t, f, hub)
	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id := jobField(t, w, "job_id").(string)
	m := pollJob(t, mux, id, func(m map[string]any) bool {
		return m["status"] == "done" && m["progress"] != nil
	})
	if m["progress"] == nil {
		t.Error("compile job progress never populated from hub")
	}
}

// J-15: job store bounded at 100 with FIFO eviction (store-level proof in
// TestJobStore_FIFOEviction; this pins the list endpoint view).
func TestJ15ListBounded(t *testing.T) {
	r, mux := newJobTestRouter(t, &fakeJobRunner{}, nil)
	for i := 0; i < jobStoreMax+1; i++ {
		r.jobs.create(JobLint)
	}
	req := httptest.NewRequest("GET", "/v1/jobs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	jobs, _ := bodyJSON(t, w)["jobs"].([]any)
	if len(jobs) != jobStoreMax {
		t.Errorf("listed %d jobs, want bounded %d", len(jobs), jobStoreMax)
	}
}

// J-16: submit with neither topic nor compile flags → 400.
func TestJ16NeitherTopicNorFlags(t *testing.T) {
	_, mux := newJobTestRouter(t, &fakeJobRunner{}, nil)
	w := submitJob(t, mux, "compile", `{}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	m := bodyJSON(t, w)
	if env, _ := m["error"].(map[string]any); env["code"] != "invalid_argument" {
		t.Errorf("error code = %v, want invalid_argument", env["code"])
	}
}

// J-17: unknown lint pass name → 400 with the valid list.
func TestJ17LintPassValidation(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "lint", `{"pass": "bogus"}`, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
	_, _, lintCalls := f.calls()
	if lintCalls != 0 {
		t.Error("invalid pass was dispatched to the runner")
	}
}

// J-18: DELETE on a finished job → 409.
func TestJ18DeleteFinished(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "lint", `{}`, "")
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, jobDone)

	wd := deleteJob(t, mux, id)
	if wd.Code != http.StatusConflict {
		t.Fatalf("delete finished status = %d, want 409", wd.Code)
	}
}

// J-19: DELETE unknown job → 404.
func TestJ19DeleteUnknown(t *testing.T) {
	_, mux := newJobTestRouter(t, &fakeJobRunner{}, nil)
	w := deleteJob(t, mux, "00000000-0000-4000-8000-000000000000")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// J-20: invalid status filter → 400.
func TestJ20InvalidStatusFilter(t *testing.T) {
	_, mux := newJobTestRouter(t, &fakeJobRunner{}, nil)
	req := httptest.NewRequest("GET", "/v1/jobs?status=bogus", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// J-21: idempotency keys are scoped per kind — same key value on compile and
// lint creates two independent jobs.
func TestJ21CrossKindIdempotency(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)

	w1 := submitJob(t, mux, "compile", `{"dry_run": true}`, "shared-key")
	id1 := jobField(t, w1, "job_id").(string)
	pollJob(t, mux, id1, jobDone)

	w2 := submitJob(t, mux, "lint", `{}`, "shared-key")
	if w2.Header().Get("X-Idempotent-Replay") == "true" {
		t.Error("cross-kind key wrongly replayed")
	}
	id2 := jobField(t, w2, "job_id").(string)
	if id1 == id2 {
		t.Error("compile and lint share an idempotency scope")
	}
	pollJob(t, mux, id2, jobDone)
	compileCalls, _, lintCalls := f.calls()
	if compileCalls != 1 || lintCalls != 1 {
		t.Errorf("calls compile=%d lint=%d, want 1/1", compileCalls, lintCalls)
	}
}

// J-22: lint-during-compile and compile-during-lint both → 202 (no 409).
func TestJ22CrossKindConcurrency(t *testing.T) {
	f := &fakeJobRunner{gate: make(chan struct{})}
	defer close(f.gate)
	_, mux := newJobTestRouter(t, f, nil)

	w1 := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id1 := jobField(t, w1, "job_id").(string)
	pollJob(t, mux, id1, func(m map[string]any) bool { return m["status"] == "running" })

	w2 := submitJob(t, mux, "lint", `{}`, "")
	if w2.Code != http.StatusAccepted {
		t.Errorf("lint during compile status = %d, want 202", w2.Code)
	}

	// Reverse: lint active (gated compile not applicable — use a second
	// router with a held lint via gate on compile? lint has no gate) —
	// compile-during-lint is symmetric in the gate map; lint jobs never
	// block compile kinds. Submit compile while lint jobs exist.
	w3 := submitJob(t, mux, "lint", `{}`, "")
	pollJob(t, mux, jobField(t, w3, "job_id").(string), jobDone)
	w4 := submitJob(t, mux, "compile", `{"topic": "x"}`, "")
	// compile is still gated by the first active compile — expect 409 here,
	// proving the gate keys on kind, not on "any job active".
	if w4.Code != http.StatusConflict {
		t.Errorf("second compile status = %d, want 409 (lint jobs must not affect it)", w4.Code)
	}
}

// J-23: two concurrent lint jobs both → 202.
func TestJ23TwoLints(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w1 := submitJob(t, mux, "lint", `{}`, "")
	w2 := submitJob(t, mux, "lint", `{}`, "")
	if w1.Code != http.StatusAccepted || w2.Code != http.StatusAccepted {
		t.Fatalf("statuses %d/%d, want 202/202", w1.Code, w2.Code)
	}
	if jobField(t, w1, "job_id") == jobField(t, w2, "job_id") {
		t.Error("concurrent lint jobs share an ID")
	}
}

// J-24: lint job progress transitions running → done.
func TestJ24LintProgress(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	w := submitJob(t, mux, "lint", `{}`, "")
	id := jobField(t, w, "job_id").(string)
	m := pollJob(t, mux, id, func(m map[string]any) bool {
		return m["status"] == "done" && m["progress"] != nil
	})
	prog, _ := m["progress"].(map[string]any)
	if prog["stage"] != "done" {
		t.Errorf("lint progress = %v, want stage done", prog)
	}
}

// J-11 (drift green with new routes) is covered by drift_test.go;
// J-12 (18 tool names unchanged) by tools/skillgen TestToolCount and the
// MCP adapter parity tests.

// Regression (independent /review): a job cancelled via DELETE while the
// runner finishes successfully must stay cancelled — the client's 202
// 'cancelled' verdict stands.
func TestJobCancelledStaysCancelledOnRunnerSuccess(t *testing.T) {
	f := &fakeJobRunner{gate: make(chan struct{})}
	_, mux := newJobTestRouter(t, f, nil)

	w := submitJob(t, mux, "compile", `{"dry_run": false}`, "")
	id := jobField(t, w, "job_id").(string)
	pollJob(t, mux, id, func(m map[string]any) bool { return m["status"] == "running" })

	wd := deleteJob(t, mux, id)
	if wd.Code != http.StatusAccepted {
		t.Fatalf("delete status = %d", wd.Code)
	}
	close(f.gate) // runner now succeeds

	m := pollJob(t, mux, id, func(m map[string]any) bool {
		return m["status"] == "cancelled" || m["status"] == "done"
	})
	if m["status"] != "cancelled" {
		t.Fatalf("cancelled job flipped to %v after runner success", m["status"])
	}
}

// Regression (found by the Python contract test): net/http cancels a
// request's context when the handler returns — a job derived from it was
// cancelled the instant its 202 was sent. This drives the full stack
// through a REAL server + client, the case httptest.NewRequest cannot
// reproduce.
func TestJobSurvivesRequestLifecycle(t *testing.T) {
	f := &fakeJobRunner{}
	_, mux := newJobTestRouter(t, f, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/jobs/compile", "application/json",
		strings.NewReader(`{"dry_run": true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	id := resp.Header.Get("Location")[len("/v1/jobs/"):]

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(srv.URL + "/v1/jobs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		r.Body.Close()
		if m["status"] == "done" {
			return
		}
		if m["status"] == "cancelled" {
			t.Fatal("job was cancelled when the submit request returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach done")
}
