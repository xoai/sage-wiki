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
	// Server slower than the budget: the unit must expire with the typed
	// per-doc timeout error (retryable — not marked compiled).
	srv := slowLLMServer(t, []time.Duration{200 * time.Millisecond}, strings.Repeat("summary ", 30))
	c, err := llm.NewClient("openai", "fake-key", srv.URL, -1)
	if err != nil {
		t.Fatal(err)
	}
	restore := llm.SetBackoffDelayForTest(func(int) time.Duration { return time.Millisecond })
	defer restore()

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
		Budgets:     NewDocBudgets(30 * time.Millisecond),
	})
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
