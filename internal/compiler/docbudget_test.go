package compiler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/pkg/events"
)

// SPEC-08 Task 12 / D6 / AC11: the per-doc compile_doc_timeout stopwatch.

func TestDocBudgetConsumeAndRemaining(t *testing.T) {
	b := NewDocBudget(100 * time.Millisecond)
	if got := b.Remaining(); got != 100*time.Millisecond {
		t.Fatalf("Remaining = %v, want 100ms", got)
	}
	b.Consume(40 * time.Millisecond)
	if got := b.Remaining(); got != 60*time.Millisecond {
		t.Fatalf("Remaining = %v, want 60ms", got)
	}
	if b.Expired() {
		t.Error("budget must not be expired at 40/100ms")
	}
	b.Consume(70 * time.Millisecond)
	if !b.Expired() {
		t.Error("budget must be expired at 110/100ms")
	}
	if b.Remaining() > 0 {
		t.Errorf("Remaining = %v, want <= 0", b.Remaining())
	}
}

func TestDocBudgetUnitContextUsesRemaining(t *testing.T) {
	b := NewDocBudget(50 * time.Millisecond)
	ctx, cancel := b.UnitContext(context.Background())
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unit context must carry a deadline")
	}
	if until := time.Until(dl); until > 60*time.Millisecond || until < 0 {
		t.Errorf("unit deadline in %v, want ~50ms", until)
	}
}

func TestDocBudgetShorterParentDeadlineWins(t *testing.T) {
	b := NewDocBudget(10 * time.Second)
	parent, pcancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer pcancel()
	ctx, cancel := b.UnitContext(parent)
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unit context must carry a deadline")
	}
	if until := time.Until(dl); until > 20*time.Millisecond {
		t.Errorf("unit deadline in %v, want ~10ms (parent wins)", until)
	}
}

func TestDocBudgetExpiredUnitContext(t *testing.T) {
	b := NewDocBudget(time.Millisecond)
	b.Consume(time.Millisecond)
	ctx, cancel := b.UnitContext(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("exhausted budget must produce an already-done context")
	}
	if !errors.Is(context.Cause(ctx), errDocBudgetDeadline) {
		t.Fatalf("context cause = %v, want errDocBudgetDeadline", context.Cause(ctx))
	}
}

func TestDocBudgetsRegistryPerDoc(t *testing.T) {
	r := NewDocBudgets(100 * time.Millisecond)
	a1 := r.For("raw/a.md")
	a2 := r.For("raw/a.md")
	if a1 != a2 {
		t.Fatal("For must be idempotent per doc")
	}
	b := r.For("raw/b.md")
	if a1 == b {
		t.Fatal("distinct docs must get distinct budgets")
	}
	a1.Consume(100 * time.Millisecond)
	if !a1.Expired() {
		t.Error("doc a budget must be expired")
	}
	if b.Expired() {
		t.Error("doc b budget must be untouched")
	}
}

func TestDocBudgetConcurrentConsume(t *testing.T) {
	b := NewDocBudget(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Consume(time.Millisecond)
			_ = b.Remaining()
			_ = b.Expired()
		}()
	}
	wg.Wait()
	if got, want := b.Remaining(), time.Hour-100*time.Millisecond; got != want {
		t.Errorf("Remaining = %v, want %v", got, want)
	}
}

// SPEC-08 AC11 integration: per-doc budget semantics through Summarize.

type budgetSink struct {
	mu     sync.Mutex
	events []events.Event
}

type contextBlockingTransport struct {
	started chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func newContextBlockingTransport() *contextBlockingTransport {
	return &contextBlockingTransport{started: make(chan struct{})}
}

func (t *contextBlockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	t.once.Do(func() { close(t.started) })
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func (t *contextBlockingTransport) waitForRequest(tb testing.TB) {
	tb.Helper()
	select {
	case <-t.started:
	case <-time.After(5 * time.Second):
		tb.Fatal("LLM request did not reach the blocking transport")
	}
}

func blockingLLMClient(t *testing.T, callTimeout time.Duration) (*llm.Client, *contextBlockingTransport) {
	t.Helper()
	c, err := llm.NewClient("openai", "fake-key", "http://llm.invalid", -1)
	if err != nil {
		t.Fatal(err)
	}
	transport := newContextBlockingTransport()
	c.SetTransport(transport)
	c.SetCallTimeout(callTimeout)
	return c, transport
}

func (s *budgetSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *budgetSink) byType(ty events.Type) []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []events.Event
	for _, ev := range s.events {
		if ev.Type == ty {
			out = append(out, ev)
		}
	}
	return out
}

func TestDocBudgetFinishUnitAttributesExactDeadlineSource(t *testing.T) {
	t.Run("budget deadline", func(t *testing.T) {
		b := NewDocBudget(time.Hour)
		unitCtx, cancel := context.WithTimeoutCause(context.Background(), 0, errDocBudgetDeadline)
		defer cancel()
		<-unitCtx.Done()

		if !b.finishUnit(unitCtx, 0, unitCtx.Err()) {
			t.Fatal("budget-owned child deadline was not attributed to the document budget")
		}
		if !errors.Is(context.Cause(unitCtx), errDocBudgetDeadline) {
			t.Fatalf("context cause = %v, want errDocBudgetDeadline", context.Cause(unitCtx))
		}
		var le *limits.LimitError
		err := docTimeoutError(b)
		if !errors.As(err, &le) {
			t.Fatalf("docTimeoutError = %v, want LimitError", err)
		}
		if le.Got < le.Limit {
			t.Fatalf("timeout accounting Got=%d, Limit=%d; want Got >= Limit", le.Got, le.Limit)
		}
	})

	t.Run("parent cancellation", func(t *testing.T) {
		b := NewDocBudget(time.Hour)
		parent, cancelParent := context.WithCancel(context.Background())
		unitCtx, cancelUnit := b.UnitContext(parent)
		defer cancelUnit()
		cancelParent()
		<-unitCtx.Done()

		if b.finishUnit(unitCtx, time.Millisecond, unitCtx.Err()) {
			t.Fatal("parent cancellation was attributed to the document budget")
		}
		if !errors.Is(context.Cause(unitCtx), context.Canceled) {
			t.Fatalf("context cause = %v, want context.Canceled", context.Cause(unitCtx))
		}
	})

	t.Run("shorter parent deadline", func(t *testing.T) {
		b := NewDocBudget(time.Hour)
		parent, cancelParent := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancelParent()
		unitCtx, cancelUnit := b.UnitContext(parent)
		defer cancelUnit()
		<-unitCtx.Done()

		if b.finishUnit(unitCtx, time.Millisecond, unitCtx.Err()) {
			t.Fatal("shorter parent deadline was attributed to the document budget")
		}
		if !errors.Is(context.Cause(unitCtx), context.DeadlineExceeded) {
			t.Fatalf("context cause = %v, want context.DeadlineExceeded", context.Cause(unitCtx))
		}
	})

	t.Run("provider timeout while unit remains live", func(t *testing.T) {
		b := NewDocBudget(time.Hour)
		unitCtx, cancelUnit := b.UnitContext(context.Background())
		defer cancelUnit()

		if b.finishUnit(unitCtx, time.Millisecond, context.DeadlineExceeded) {
			t.Fatal("provider timeout was attributed to the document budget")
		}
		if unitCtx.Err() != nil {
			t.Fatalf("unit context unexpectedly terminated: %v", unitCtx.Err())
		}
	})

	t.Run("explicit cleanup cancellation", func(t *testing.T) {
		b := NewDocBudget(time.Hour)
		unitCtx, cancelUnit := b.UnitContext(context.Background())
		cancelUnit()
		<-unitCtx.Done()

		if b.finishUnit(unitCtx, time.Millisecond, unitCtx.Err()) {
			t.Fatal("cleanup cancellation was attributed to the document budget")
		}
	})
}

func TestEmitDocTimeoutEventPair(t *testing.T) {
	sink := &budgetSink{}
	b := NewDocBudget(10 * time.Millisecond)
	b.Consume(15 * time.Millisecond)
	emitDocTimeout(sink, "/tmp/ws", "job-1", "raw/a.md", docTimeoutError(b))

	finished := sink.byType(events.TypeCompileDocFinished)
	if len(finished) != 1 {
		t.Fatalf("compile_doc_finished events = %d, want 1", len(finished))
	}
	if p, ok := finished[0].Data.(events.CompileDocFinished); !ok || !p.Skipped || p.DocID != "raw/a.md" {
		t.Errorf("compile_doc_finished payload = %+v, want Skipped=true for raw/a.md", finished[0].Data)
	}
	exceeded := sink.byType(events.TypeLimitExceeded)
	if len(exceeded) != 1 {
		t.Fatalf("limit_exceeded events = %d, want 1", len(exceeded))
	}
	if p, ok := exceeded[0].Data.(events.LimitExceeded); !ok || p.Which != "compile_doc_timeout" {
		t.Errorf("limit_exceeded payload = %+v, want Which=compile_doc_timeout", exceeded[0].Data)
	}
}

// slowLLMServer returns chat completions after a per-request delay sequence.
func slowLLMServer(t *testing.T, delays []time.Duration, body string) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		d := delays[len(delays)-1]
		if i < len(delays) {
			d = delays[i]
		}
		time.Sleep(d)
		w.Write([]byte(`{"choices":[{"message":{"content":"` + body + `"}}],"usage":{"total_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func budgetWorkspace(t *testing.T, names ...string) (projectDir, outputDir string, sources []SourceInfo) {
	t.Helper()
	projectDir = t.TempDir()
	outputDir = filepath.Join(projectDir, "wiki")
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0o755)
	os.MkdirAll(outputDir, 0o755)
	for _, n := range names {
		p := filepath.Join("raw", n)
		if err := os.WriteFile(filepath.Join(projectDir, p), []byte("# "+n+"\n\nSome content to summarize for the test."), 0o644); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, SourceInfo{Path: p, Type: "article"})
	}
	return projectDir, outputDir, sources
}

func TestSummarizeDocBudgetExpiryTyped(t *testing.T) {
	// Context completion itself releases the transport; no server sleep decides
	// whether the document budget owns the timeout.
	c, transport := blockingLLMClient(t, time.Hour)

	projectDir, outputDir, sources := budgetWorkspace(t, "a.md")
	results := Summarize(SummarizeOpts{
		Ctx:         context.Background(),
		ProjectDir:  projectDir,
		OutputDir:   outputDir,
		Sources:     sources,
		Client:      c,
		Model:       "gpt-4o-mini",
		MaxTokens:   256,
		MaxParallel: 1,
		UserTZ:      time.UTC,
		Budgets:     NewDocBudgets(10 * time.Millisecond),
	})
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !errors.Is(results[0].Error, limits.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", results[0].Error)
	}
	var le *limits.LimitError
	if !errors.As(results[0].Error, &le) || le.Which != "compile_doc_timeout" {
		t.Errorf("error is not a compile_doc_timeout LimitError: %v", results[0].Error)
	}
	if le.Got < le.Limit {
		t.Errorf("timeout accounting Got=%d, Limit=%d; want Got >= Limit", le.Got, le.Limit)
	}
}

func TestSummarizeQueueTimeNotConsumed(t *testing.T) {
	// MaxParallel 1: source B queues behind A. Queue wait must NOT consume
	// B's budget — B gets its full budget and succeeds even though it starts
	// late. (If queueing consumed budget, B's 50ms unit would overrun its
	// remaining 40ms and expire.)
	srv := slowLLMServer(t, []time.Duration{60 * time.Millisecond, 50 * time.Millisecond}, strings.Repeat("summary ", 30))
	c, err := llm.NewClient("openai", "fake-key", srv.URL, -1)
	if err != nil {
		t.Fatal(err)
	}
	restore := llm.SetBackoffDelayForTest(func(int) time.Duration { return time.Millisecond })
	defer restore()

	projectDir, outputDir, sources := budgetWorkspace(t, "a.md", "b.md")
	results := Summarize(SummarizeOpts{
		Ctx:         context.Background(),
		ProjectDir:  projectDir,
		OutputDir:   outputDir,
		Sources:     sources,
		Client:      c,
		Model:       "gpt-4o-mini",
		MaxTokens:   256,
		MaxParallel: 1,
		UserTZ:      time.UTC,
		Budgets:     NewDocBudgets(100 * time.Millisecond),
	})
	for i, r := range results {
		if errors.Is(r.Error, limits.ErrTimeout) {
			t.Errorf("source %d expired — queue time leaked into its budget: %v", i, r.Error)
		}
	}
}

// TestSummarizeProviderTimeoutNotMisattributed (SPEC-08 AC11 review fix):
// provider_timeout (the per-call LLM deadline, default 120s) fires as
// context.DeadlineExceeded — the SAME error class as a compile_doc_timeout
// budget expiry. A provider timeout with budget REMAINING must NOT be
// reclassified as compile_doc_timeout (it would emit a self-contradictory
// limit_exceeded{Limit=compile_doc_budget > Got=provider_timeout} and
// inflate the wrong counter). Only a deadline that ALSO exhausted the
// per-doc budget is a compile_doc_timeout.
func TestSummarizeProviderTimeoutNotMisattributed(t *testing.T) {
	c, transport := blockingLLMClient(t, time.Millisecond)

	projectDir, outputDir, sources := budgetWorkspace(t, "a.md")
	results := Summarize(SummarizeOpts{
		Ctx:         context.Background(),
		ProjectDir:  projectDir,
		OutputDir:   outputDir,
		Sources:     sources,
		Client:      c,
		Model:       "gpt-4o-mini",
		MaxTokens:   256,
		MaxParallel: 1,
		UserTZ:      time.UTC,
		Budgets:     NewDocBudgets(time.Hour),
	})
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("want a provider-timeout error, got nil")
	}
	// The provider timeout must NOT be classified as compile_doc_timeout.
	if errors.Is(results[0].Error, limits.ErrTimeout) {
		t.Errorf("provider timeout misattributed as compile_doc_timeout (budget was not exhausted): %v", results[0].Error)
	}
}

func TestSummarizeParentCancelWithBudgetPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, transport := blockingLLMClient(t, time.Hour)
	projectDir, outputDir, sources := budgetWorkspace(t, "a.md")

	resultCh := make(chan []SummaryResult, 1)
	go func() {
		resultCh <- Summarize(SummarizeOpts{
			Ctx:         ctx,
			ProjectDir:  projectDir,
			OutputDir:   outputDir,
			Sources:     sources,
			Client:      c,
			Model:       "gpt-4o-mini",
			MaxTokens:   256,
			MaxParallel: 1,
			UserTZ:      time.UTC,
			Budgets:     NewDocBudgets(time.Hour),
		})
	}()

	transport.waitForRequest(t)
	cancel()
	results := <-resultCh
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !errors.Is(results[0].Error, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", results[0].Error)
	}
	if errors.Is(results[0].Error, limits.ErrTimeout) {
		t.Fatalf("parent cancellation was misattributed as compile_doc_timeout: %v", results[0].Error)
	}
}
