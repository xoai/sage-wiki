package compiler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/pkg/events"
)

// captureEventSink collects events in arrival order.
type captureEventSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *captureEventSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *captureEventSink) snapshot() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]events.Event{}, s.events...)
}

func (s *captureEventSink) byType(t events.Type) []events.Event {
	var out []events.Event
	for _, ev := range s.snapshot() {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

// TestCompileLifecycleEvents: a tier-3 compile brackets Started→Finished
// with the injected JobID, emits per-doc outcomes, and bridges usage —
// all with opts.Recorder nil (the compiler is the single recorder builder).
func TestCompileLifecycleEvents(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	sink := &captureEventSink{}
	dir := t.TempDir()
	writeDeferredCorpusInto(t, dir, srv.URL)
	res, err := Compile(dir, CompileOpts{Sink: sink, JobID: "job-test-1"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.JobID != "job-test-1" {
		t.Errorf("CompileResult.JobID = %q, want the injected ID", res.JobID)
	}

	started := sink.byType(events.TypeCompileStarted)
	finished := sink.byType(events.TypeCompileFinished)
	if len(started) != 1 || len(finished) != 1 {
		t.Fatalf("started=%d finished=%d, want 1 each (events: %d total)", len(started), len(finished), len(sink.snapshot()))
	}
	if got := started[0].Data.(events.CompileStarted); got.JobID != "job-test-1" {
		t.Errorf("CompileStarted.JobID = %q", got.JobID)
	}
	fin := finished[0].Data.(events.CompileFinished)
	if fin.JobID != "job-test-1" {
		t.Errorf("CompileFinished.JobID = %q", fin.JobID)
	}
	if fin.Outcome != "completed" {
		t.Errorf("Outcome = %q, want completed", fin.Outcome)
	}
	if fin.Totals.Docs != 3 || fin.Totals.Compiled == 0 {
		t.Errorf("Totals = %+v, want 3 docs and some compiled", fin.Totals)
	}
	if started[0].Workspace == "" || finished[0].Workspace == "" {
		t.Error("events must carry the workspace name")
	}

	// Per-doc outcomes: each doc completes every tier phase it passes
	// through (0 index → 1 embed → 3 full pipeline) — one event per
	// tier completion, 3 docs × 3 tiers.
	docs := sink.byType(events.TypeCompileDocFinished)
	if len(docs) != 9 {
		t.Fatalf("compile_doc_finished = %d, want 9 (3 docs × 3 tier phases)", len(docs))
	}
	perTier := map[int]int{}
	for _, ev := range docs {
		d := ev.Data.(events.CompileDocFinished)
		if d.JobID != "job-test-1" || d.Skipped {
			t.Errorf("doc event = %+v", d)
		}
		perTier[d.Tier]++
	}
	for _, tier := range []int{0, 1, 3} {
		if perTier[tier] != 3 {
			t.Errorf("tier %d events = %d, want 3", tier, perTier[tier])
		}
	}

	// Usage bridge: the recorder construction used the sink (Recorder nil).
	if len(sink.byType(events.TypeUsage)) == 0 {
		t.Error("no usage events reached the sink (bridge broken)")
	}
}

// TestCompileSkipEvents: an unchanged recompile emits one compile_skip and
// one compile_doc_finished(skipped) per doc — the SPEC-04 pledge, emitted
// where the verdict is made (migrated from pkg/engine).
func TestCompileSkipEvents(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	sink := &captureEventSink{}
	res := compileInDir(t, dir, srv.URL, CompileOpts{Sink: sink, JobID: "job-skip"})
	if len(res.Skipped) != 3 {
		t.Fatalf("Skipped = %d, want 3", len(res.Skipped))
	}

	skips := sink.byType(events.TypeCompileSkip)
	if len(skips) != 3 {
		t.Fatalf("compile_skip events = %d, want 3", len(skips))
	}
	for _, ev := range skips {
		d := ev.Data.(events.CompileSkip)
		if d.DocID == "" || d.Reason == "" {
			t.Errorf("skip event payload = %+v", d)
		}
	}

	var skippedDocs int
	for _, ev := range sink.byType(events.TypeCompileDocFinished) {
		if ev.Data.(events.CompileDocFinished).Skipped {
			skippedDocs++
		}
	}
	if skippedDocs != 3 {
		t.Errorf("skipped compile_doc_finished = %d, want 3", skippedDocs)
	}
}

// TestEmitCompileFinishedOutcome: the Outcome mapping — completed, failed,
// cancelled, and interrupted (serve-queue stop marker).
func TestEmitCompileFinishedOutcome(t *testing.T) {
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	interrupted := true
	cases := []struct {
		name          string
		ctx           context.Context
		err           error
		isInterrupted func() bool
		want          string
	}{
		{"success", context.Background(), nil, nil, "completed"},
		{"error", context.Background(), errors.New("boom"), nil, "failed"},
		{"cancel", canceledCtx, context.Canceled, nil, "cancelled"},
		{"interrupted", canceledCtx, context.Canceled, func() bool { return interrupted }, "interrupted"},
		// A nil ctx can never have been cancelled — callers that omit Ctx
		// (hub, TUI) must map errors to failed, not cancelled.
		{"nil ctx error", nil, errors.New("boom"), nil, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &captureEventSink{}
			run := &compileRun{
				opts: CompileOpts{
					Sink: sink, JobID: "job-o", Ctx: tc.ctx, IsInterrupted: tc.isInterrupted,
				},
				result:         &CompileResult{},
				startedEmitted: true,
			}
			run.emitCompileFinished(t.TempDir(), &CompileResult{}, tc.err)
			fin := sink.byType(events.TypeCompileFinished)
			if len(fin) != 1 {
				t.Fatalf("finished events = %d, want 1", len(fin))
			}
			if got := fin[0].Data.(events.CompileFinished).Outcome; got != tc.want {
				t.Errorf("Outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEmitCompileFinishedPairs: no CompileStarted → no CompileFinished
// (early loadInputs returns emit nothing — pairs, never orphans).
func TestEmitCompileFinishedPairs(t *testing.T) {
	sink := &captureEventSink{}
	run := &compileRun{
		opts:   CompileOpts{Sink: sink, JobID: "job-p"},
		result: &CompileResult{},
	}
	run.emitCompileFinished(t.TempDir(), &CompileResult{}, errors.New("early failure"))
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("events = %d, want 0 (never started)", got)
	}
}

// TestCompileEmitsEdgeEvents is the Milestone-2 seam proof: EdgeAdded
// events from the ontology store reach the run's sink through a REAL
// compile — proving CompileOpts.Sink is injected at every compiler-internal
// store acquisition site (sqlite path; the postgres twin carries the same
// setter, env-gated in CI).
func TestCompileEmitsEdgeEvents(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	sink := &captureEventSink{}
	dir := t.TempDir()
	writeDeferredCorpusInto(t, dir, srv.URL)
	if _, err := Compile(dir, CompileOpts{Sink: sink, JobID: "job-edges"}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	edges := sink.byType(events.TypeEdgeAdded)
	if len(edges) == 0 {
		t.Fatal("no edge_added events reached the sink — store injection broken")
	}
	for _, ev := range edges {
		d := ev.Data.(events.EdgeAdded)
		if d.EdgeID == "" || d.Relation == "" || d.From == "" || d.To == "" {
			t.Errorf("edge event payload incomplete: %+v", d)
		}
		if ev.Workspace == "" {
			t.Error("edge event must carry the workspace name (BindWorkspace)")
		}
	}
}

// TestEmitPromotion: promotion_triggered carries both directions and the
// trigger label.
func TestEmitPromotion(t *testing.T) {
	sink := &captureEventSink{}
	run := &compileRun{opts: CompileOpts{Sink: sink, JobID: "job-promo"}, result: &CompileResult{}}
	run.emitPromotion(t.TempDir(), "raw/a.md", 1, 3, "auto-promote")
	run.emitPromotion(t.TempDir(), "raw/b.md", 3, 1, "stale")

	got := sink.byType(events.TypePromotionTriggered)
	if len(got) != 2 {
		t.Fatalf("promotion events = %d, want 2", len(got))
	}
	up := got[0].Data.(events.PromotionTriggered)
	down := got[1].Data.(events.PromotionTriggered)
	if up.FromTier != 1 || up.ToTier != 3 || up.Trigger != "auto-promote" {
		t.Errorf("promotion = %+v", up)
	}
	if down.FromTier != 3 || down.ToTier != 1 || down.Trigger != "stale" {
		t.Errorf("demotion = %+v", down)
	}
}

// sleepingSink stalls in Emit — the deliberately-pathological sink of the
// SPEC-07 compile-throughput guard (AC-2, plan Task 10).
type sleepingSink struct {
	delay time.Duration
}

func (s sleepingSink) Emit(events.Event) {
	time.Sleep(s.delay)
}

// TestCompileThroughputWithSlowSink (AC-2 gate, compile level): a stalled
// sink must not slow compile throughput. With the deferred corpus the run
// emits well over 100 events; a blocking design would add ≥ events×5ms ≈
// 0.5s+. The assertion is a wall-clock bound with two orders of headroom —
// deterministic, no benchmark-comparison noise.
func TestCompileThroughputWithSlowSink(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	compileWith := func(sink events.Sink) time.Duration {
		dir := t.TempDir()
		writeDeferredCorpusInto(t, dir, srv.URL)
		start := time.Now()
		if _, err := Compile(dir, CompileOpts{Sink: sink, JobID: "job-slow"}); err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return time.Since(start)
	}

	baseline := compileWith(nil)
	withSlow := compileWith(sleepingSink{delay: 5 * time.Millisecond})

	// A blocking engine pays 5ms per emitted event (100+ events → 500ms+).
	// Allow the slow-sink run at most 3× the baseline plus a fixed 150ms
	// scheduling budget — far under the blocking cost, far above noise.
	if bound := 3*baseline + 150*time.Millisecond; withSlow > bound {
		t.Fatalf("compile with a 5ms-slow sink took %s (baseline %s, bound %s) — sink stalled the pipeline",
			withSlow, baseline, bound)
	}
}
