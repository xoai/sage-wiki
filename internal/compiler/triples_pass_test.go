package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

func passStore(t *testing.T) *ontology.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "pass.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.MergedRelations(nil)),
		ontology.ValidEntityTypeNames(ontology.MergedEntityTypes(nil)))
}

// countingServer replies with sampleGraph and counts requests.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": sampleGraph}}},
			"model":   "m",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func enabledCfg() *config.Config {
	c := &config.Config{}
	c.Ontology.Triples.Enabled = true
	c.Models.Extract = "m"
	return c
}

// Default-off is the whole opt-in contract: an upgrade must cost nothing.
func TestExtractTriplesPassDisabledMakesNoCall(t *testing.T) {
	srv, calls := countingServer(t)
	ont := passStore(t)

	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		&config.Config{}, triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 when triples are disabled", got)
	}
	if n, _ := ont.RelationCount(); n != 0 {
		t.Errorf("relations = %d, want 0", n)
	}
}

// A zero-valued config must still work. Defaults() has no Ontology entry and is
// only reached via config.Load, so a Config{} literal yields zero caps AND
// MaxParallel 0 — and an unbuffered semaphore whose only receiver is its own
// deferred release is a permanent hang, not a slow path. The timeout is what
// makes that a failure instead of a wedged CI job.
func TestExtractTriplesPassZeroValuedConfigStillExtracts(t *testing.T) {
	srv, calls := countingServer(t)
	ont := passStore(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ExtractTriplesPass(context.Background(), ont,
			[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
			enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("pass hung with a zero-valued config — MaxParallel must be floored at 1")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("LLM calls = %d, want 1", got)
	}
	if n, _ := ont.RelationCount(); n != 1 {
		t.Errorf("relations = %d, want 1 — zero caps must default, not zero the pass", n)
	}
}

// opts.Ctx is nilable at the fullpipeline call site (fullpipeline.go proves it
// seven lines later), and a per-document ctx.Err() on a nil interface panics.
func TestExtractTriplesPassToleratesNilContext(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)

	//nolint:staticcheck // deliberately nil: the call site can pass a nil ctx.
	ExtractTriplesPass(nil, ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	if n, _ := ont.RelationCount(); n != 1 {
		t.Errorf("relations = %d, want 1", n)
	}
}

// A provider outage must not fail the compile: this is an additive, opt-in
// enrichment pass, and articles must still be written.
func TestExtractTriplesPassContainsProviderFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	ont := passStore(t)

	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	if n, _ := ont.RelationCount(); n != 0 {
		t.Errorf("relations = %d, want 0", n)
	}
}

// Cancellation must read as cancellation, not as one failure per remaining
// document — the pass swallows errors, so without a distinct line a Ctrl-C is
// indistinguishable from a provider outage.
func TestExtractTriplesPassLogsCancellationOnce(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)

	var buf strings.Builder
	var mu sync.Mutex
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(
		&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	summaries := make([]SummaryResult, 5)
	for i := range summaries {
		summaries[i] = SummaryResult{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}
	}
	ExtractTriplesPass(ctx, ont, summaries, nil, enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	mu.Lock()
	out := buf.String()
	mu.Unlock()

	cancelled := strings.Count(out, "cancel")
	failures := strings.Count(out, "failed")
	if cancelled != 1 {
		t.Errorf("cancellation lines = %d, want exactly 1:\n%s", cancelled, out)
	}
	if failures != 0 {
		t.Errorf("failure lines = %d, want 0 on a cancel:\n%s", failures, out)
	}
}

// lockedWriter serializes writes from the pass's goroutines.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// The label must be restored, or every token spent after this pass is billed to
// it — on the re-extract path that is the entire write pass.
func TestExtractTriplesPassRestoresCostLabel(t *testing.T) {
	srv, _ := countingServer(t)
	client := triplesClient(t, srv.URL)
	client.SetPass("extract")

	ExtractTriplesPass(context.Background(), passStore(t),
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), client, false, t.TempDir(), nil, nil, nil, nil, nil)

	if got := client.Pass(); got != "extract" {
		t.Errorf("Pass() = %q after the triples pass, want the prior label restored", got)
	}
}

// models.extract is commonly empty; stopping the chain there sends an empty
// model string to the provider.
func TestExtractTriplesPassModelChainFallsBackToSummarize(t *testing.T) {
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": sampleGraph}}},
			"model":   "m", "usage": map[string]int{"total_tokens": 1},
		})
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Ontology.Triples.Enabled = true
	cfg.Models.Summarize = "summarize-model" // Extract deliberately empty

	ExtractTriplesPass(context.Background(), passStore(t),
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		cfg, triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	mu.Lock()
	defer mu.Unlock()
	if len(models) != 1 || models[0] != "summarize-model" {
		t.Errorf("model sent = %v, want [summarize-model] via the extract->summarize fallback", models)
	}
}

// On the re-extract path the summary carries its own frontmatter: SourceDoc
// must come from the `source:` key, and the frontmatter must not reach the
// model (an evidence span could otherwise be quoted out of `compiled_at:`).
func TestExtractTriplesPassResolvesSourceDocFromFrontmatter(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		for _, m := range body.Messages {
			prompts = append(prompts, m.Content)
		}
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": sampleGraph}}},
			"model":   "m", "usage": map[string]int{"total_tokens": 1},
		})
	}))
	defer srv.Close()
	ont := passStore(t)

	// The five-key shape summarize.go writes.
	withFM := "---\nsource: raw/real-source.pdf\nsource_type: pdf\nsource_hash: abc\n" +
		"compiled_at: 2026-01-01T00:00:00Z\nchunk_count: 3\n---\n\nBackpressure extends flow control.\n"
	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "some-summary.md", Summary: withFM}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), true, t.TempDir(), nil, nil, nil, nil, nil)

	rels, _ := ont.GetRelations("backpressure", ontology.Outbound, "")
	if len(rels) != 1 {
		t.Fatalf("relations = %d, want 1", len(rels))
	}
	if rels[0].SourceDoc != "raw/real-source.pdf" {
		t.Errorf("SourceDoc = %q, want the frontmatter source, not the summary filename", rels[0].SourceDoc)
	}

	mu.Lock()
	joined := strings.Join(prompts, "\n")
	mu.Unlock()
	if strings.Contains(joined, "compiled_at:") || strings.Contains(joined, "source_hash:") {
		t.Error("summary frontmatter reached the model; evidence could be quoted out of it")
	}
}

// The batch path writes a different, three-key frontmatter.
func TestExtractTriplesPassParsesBatchFrontmatter(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)

	batchFM := "---\nsource: raw/batch.md\ncompiled_at: 2026-01-01T00:00:00Z\nbatch: true\n---\n\nBackpressure extends flow control.\n"
	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "b.md", Summary: batchFM}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), true, t.TempDir(), nil, nil, nil, nil, nil)

	rels, _ := ont.GetRelations("backpressure", ontology.Outbound, "")
	if len(rels) != 1 || rels[0].SourceDoc != "raw/batch.md" {
		t.Errorf("SourceDoc from batch frontmatter: %+v", rels)
	}
}

// On the normal path SourcePath is already correct, and a summary body that
// merely opens with a `---` rule must not be mistaken for frontmatter.
func TestExtractTriplesPassUsesSourcePathWithoutFrontmatterFlag(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)

	body := "---\n\nBackpressure extends flow control.\n"
	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/normal.md", Summary: body}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	rels, _ := ont.GetRelations("backpressure", ontology.Outbound, "")
	if len(rels) != 1 || rels[0].SourceDoc != "raw/normal.md" {
		t.Errorf("SourceDoc = %+v, want raw/normal.md", rels)
	}
}

// The fan-out is only meaningful if a test actually runs it concurrently:
// every other pass test uses one summary and a zero MaxParallel (floored to 1),
// so -race never sees two goroutines in this pass.
func TestExtractTriplesPassFansOutConcurrently(t *testing.T) {
	var inFlight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		defer inFlight.Add(-1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": sampleGraph}}},
			"model":   "m", "usage": map[string]int{"total_tokens": 1},
		})
	}))
	defer srv.Close()

	cfg := enabledCfg()
	cfg.Compiler.MaxParallel = 8

	summaries := make([]SummaryResult, 24)
	for i := range summaries {
		summaries[i] = SummaryResult{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}
	}
	ont := passStore(t)
	ExtractTriplesPass(context.Background(), ont, summaries, nil, cfg, triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	if peak.Load() < 2 {
		t.Errorf("peak concurrent requests = %d, want >1 — the fan-out never ran in parallel", peak.Load())
	}
	if got := peak.Load(); got > 8 {
		t.Errorf("peak concurrent requests = %d, want <= MaxParallel (8)", got)
	}
	// All 24 documents assert the same edge; the upsert collapses them to one.
	if n, _ := ont.RelationCount(); n != 1 {
		t.Errorf("RelationCount = %d, want 1", n)
	}
}

// Cancellation MID-FLIGHT: the pre-check catches a ctx cancelled before the
// call, but the branch that classifies an in-flight provider error as
// cancellation rather than a per-document failure is only reachable this way.
func TestExtractTriplesPassParentCancelWithBudgetPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := triplesClient(t, "http://llm.invalid")
	transport := newContextBlockingTransport()
	client.SetTransport(transport)
	client.SetCallTimeout(time.Hour)

	var buf strings.Builder
	var mu sync.Mutex
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(
		&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer restore()

	done := make(chan []tripleTimeout, 1)
	go func() {
		_, _, timeouts := ExtractTriplesPass(ctx, passStore(t),
			[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
			enabledCfg(), client, false, t.TempDir(), nil, nil, nil, NewDocBudgets(time.Hour), nil)
		done <- timeouts
	}()

	transport.waitForRequest(t)
	cancel()
	timeouts := <-done
	if len(timeouts) != 0 {
		t.Fatalf("timeouts = %+v, want none for parent cancellation", timeouts)
	}

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "cancel") {
		t.Errorf("a mid-flight cancel was not reported as cancellation:\n%s", out)
	}
	if strings.Contains(out, "extraction failed") {
		t.Errorf("a mid-flight cancel was misreported as a per-document failure:\n%s", out)
	}
}

// Cancellation must not throw away extractions already paid for.
func TestExtractTriplesPassKeepsCompletedWorkOnCancel(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": sampleGraph}}},
				"model":   "m", "usage": map[string]int{"total_tokens": 1},
			})
			return
		}
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := enabledCfg()
	cfg.Compiler.MaxParallel = 1 // deterministic ordering: doc 1 completes, then cancel

	ont := passStore(t)
	ExtractTriplesPass(ctx, ont, []SummaryResult{
		{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."},
		{SourcePath: "raw/b.md", Summary: "Backpressure extends flow control."},
	}, nil, cfg, triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	if n, _ := ont.RelationCount(); n != 1 {
		t.Errorf("RelationCount = %d, want 1 — the completed extraction was discarded on cancel", n)
	}
}

// A provider outage must be reported, not merely survived: asserting only
// "no relations written" is also true of a pass that never ran.
func TestExtractTriplesPassReportsProviderFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()

	var buf strings.Builder
	var mu sync.Mutex
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(
		&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer restore()

	ExtractTriplesPass(context.Background(), passStore(t),
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, nil)

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "extraction failed") {
		t.Errorf("provider failure not logged per document:\n%s", out)
	}
	if !strings.Contains(out, "some documents failed extraction") {
		t.Errorf("failure count not summarized:\n%s", out)
	}
}

// A summary that is only frontmatter must not buy an LLM call.
func TestExtractTriplesPassSkipsEmptyBodies(t *testing.T) {
	srv, calls := countingServer(t)
	onlyFrontmatter := "---\nsource: raw/a.md\ncompiled_at: 2026-01-01T00:00:00Z\nbatch: true\n---\n\n\n"

	ExtractTriplesPass(context.Background(), passStore(t),
		[]SummaryResult{{SourcePath: "s.md", Summary: onlyFrontmatter}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), true, t.TempDir(), nil, nil, nil, nil, nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 for a frontmatter-only summary", got)
	}
}

// CRLF summaries must parse: a Windows-authored summary would otherwise miss
// the frontmatter and stamp the summary filename as SourceDoc.
func TestExtractTriplesPassParsesCRLFFrontmatter(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)
	crlf := "---\r\nsource: raw/windows.md\r\ncompiled_at: 2026-01-01T00:00:00Z\r\nbatch: true\r\n---\r\n\r\nBackpressure extends flow control.\r\n"

	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "s.md", Summary: crlf}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), true, t.TempDir(), nil, nil, nil, nil, nil)

	rels, _ := ont.GetRelations("backpressure", ontology.Outbound, "")
	if len(rels) != 1 || rels[0].SourceDoc != "raw/windows.md" {
		t.Errorf("CRLF frontmatter not parsed: %+v", rels)
	}
}

// TestExtractTriplesPassBudgetExpiryRecorded (SPEC-08 AC11): a doc whose
// per-doc budget is exhausted BEFORE its triples unit runs must be recorded
// as a per-doc timeout (not a silent failure), make NO paid call, and surface
// the typed compile_doc_timeout error — the caller emits the event pair and
// marks the run incomplete so the doc is retried.
func TestExtractTriplesPassBudgetExpiryRecorded(t *testing.T) {
	srv, calls := countingServer(t)
	ont := passStore(t)

	// Pre-exhaust the doc's budget so the worker's Expired() gate fires
	// before any LLM call.
	budgets := NewDocBudgets(time.Second)
	budgets.For("raw/a.md").Consume(2 * time.Second)

	_, _, timeouts := ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, budgets, nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 (budget pre-expired — no paid call)", got)
	}
	if len(timeouts) != 1 || timeouts[0].SourcePath != "raw/a.md" {
		t.Fatalf("timeouts = %+v, want exactly one raw/a.md entry", timeouts)
	}
	if !errors.Is(timeouts[0].Err, limits.ErrTimeout) {
		t.Errorf("timeout err = %v, want errors.Is ErrTimeout", timeouts[0].Err)
	}
	var le *limits.LimitError
	if !errors.As(timeouts[0].Err, &le) || le.Which != limits.WhichCompileDocTimeout {
		t.Errorf("err = %v, want a compile_doc_timeout LimitError", timeouts[0].Err)
	}
	if n, _ := ont.RelationCount(); n != 0 {
		t.Errorf("relations = %d, want 0 (timed-out doc persisted nothing)", n)
	}
}

// TestExtractTriplesPassBudgetExpiryMidCall (SPEC-08 AC11): when the budget
// deadline fires DURING the LLM call (not before it), the post-call branch
// must still classify the failure as a per-doc timeout and surface it — not
// as a generic provider failure. The deadline surfaces as
// context.DeadlineExceeded through the client.
func TestExtractTriplesPassBudgetExpiryMidCall(t *testing.T) {
	client := triplesClient(t, "http://llm.invalid")
	transport := newContextBlockingTransport()
	client.SetTransport(transport)
	client.SetCallTimeout(time.Hour)
	ont := passStore(t)

	budgets := NewDocBudgets(10 * time.Millisecond)
	_, _, timeouts := ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), client, false, t.TempDir(), nil, nil, nil, budgets, nil)

	if len(timeouts) != 1 {
		t.Fatalf("timeouts = %+v, want one mid-call timeout entry", timeouts)
	}
	if !errors.Is(timeouts[0].Err, limits.ErrTimeout) {
		t.Errorf("mid-call err = %v, want errors.Is ErrTimeout (not a generic failure)", timeouts[0].Err)
	}
	var le *limits.LimitError
	if !errors.As(timeouts[0].Err, &le) || le.Got < le.Limit {
		t.Errorf("mid-call timeout = %v, want LimitError with Got >= Limit", timeouts[0].Err)
	}
}

func TestExtractTriplesPassProviderTimeoutNotMisattributed(t *testing.T) {
	client := triplesClient(t, "http://llm.invalid")
	transport := newContextBlockingTransport()
	client.SetTransport(transport)
	client.SetCallTimeout(time.Millisecond)

	_, _, timeouts := ExtractTriplesPass(context.Background(), passStore(t),
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Backpressure extends flow control."}}, nil,
		enabledCfg(), client, false, t.TempDir(), nil, nil, nil, NewDocBudgets(time.Hour), nil)

	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	if len(timeouts) != 0 {
		t.Fatalf("provider timeout was misattributed as compile_doc_timeout: %+v", timeouts)
	}
}
