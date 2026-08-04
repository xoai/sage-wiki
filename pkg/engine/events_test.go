package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/pkg/events"
)

type captureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *captureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

// TestEventSinkReceivesCompileUsage: a compile through the engine emits
// usage events to BOTH the workspace ledger and the installed sink.
func TestEventSinkReceivesCompileUsage(t *testing.T) {
	srv := stubLLM(t, "gpt-4o-mini")
	dir := initWorkspace(t)
	extra := `version: 1
project: ws
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + srv.URL + `
models:
  summarize: gpt-4o-mini
  extract: gpt-4o-mini
  write: gpt-4o-mini
compiler:
  auto_commit: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "article.md"), []byte("# Attention\n\nSelf-attention computes contextual representations."), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &captureSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Compile(context.Background(), CompileRequest{Selector: "pending", Tier: 3}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) == 0 {
		t.Fatal("sink received no events")
	}
	// The stream now opens with compile lifecycle events (SPEC-07); find
	// the usage event among them.
	var ev events.Event
	found := false
	for _, e := range sink.events {
		if e.Type == events.TypeUsage {
			ev = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no usage event in %+v", sink.events)
	}
	if ev.Type != events.TypeUsage {
		t.Fatalf("event type = %s, want %s", ev.Type, events.TypeUsage)
	}
	usage, ok := ev.Data.(events.Usage)
	if !ok {
		t.Fatalf("event data = %T, want events.Usage", ev.Data)
	}
	if usage.Provider != "openai" || usage.Model != "gpt-4o-mini" {
		t.Errorf("usage = %+v", usage)
	}
	if usage.Tier != 3 {
		t.Errorf("Tier = %d, want 3 (compile-scoped)", usage.Tier)
	}
	if usage.Cost == nil {
		t.Error("gpt-4o-mini is priced — Cost must be set")
	}
	if ev.Workspace != filepath.Base(dir) {
		t.Errorf("Workspace = %q, want %q (name only, SPEC-07)", ev.Workspace, filepath.Base(dir))
	}

	// The file ledger has the same events.
	ledger, err := llm.ReadUsageLog(filepath.Join(dir, ".sage", "usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) == 0 {
		t.Error("file ledger must also carry the events")
	}
}

// TestCaptureEmitsDocCaptured: a Reader capture emits doc_captured with the
// workspace-relative DocID, stored size, and content hash — no paths.
func TestCaptureEmitsDocCaptured(t *testing.T) {
	dir := initWorkspace(t)
	sink := &captureSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	id, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("Captured doc body.")})
	if err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var found *events.DocCaptured
	var wsName string
	for _, ev := range sink.events {
		if ev.Type == events.TypeDocCaptured {
			d := ev.Data.(events.DocCaptured)
			found = &d
			wsName = ev.Workspace
		}
	}
	if found == nil {
		t.Fatalf("no doc_captured event in %+v", sink.events)
	}
	if found.DocID != string(id) {
		t.Errorf("DocID = %q, want %q", found.DocID, id)
	}
	if !strings.HasPrefix(found.ContentHash, "sha256:") {
		t.Errorf("ContentHash = %q, want sha256-prefixed", found.ContentHash)
	}
	if found.Bytes == 0 {
		t.Error("Bytes must be non-zero")
	}
	if wsName != filepath.Base(dir) {
		t.Errorf("Workspace = %q, want name %q", wsName, filepath.Base(dir))
	}
}

// TestSearchEmitsSearchPerformed: a search emits search_performed with the
// HASHED query (never raw), channels, and result count.
func TestSearchEmitsSearchPerformed(t *testing.T) {
	dir := initWorkspace(t)
	sink := &captureSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Search(context.Background(), SearchRequest{Query: "  what is attention  "}); err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var found *events.SearchPerformed
	for _, ev := range sink.events {
		if ev.Type == events.TypeSearchPerformed {
			d := ev.Data.(events.SearchPerformed)
			found = &d
		}
	}
	if found == nil {
		t.Fatalf("no search_performed event in %+v", sink.events)
	}
	wantHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("what is attention")))
	if found.QueryHash != wantHash {
		t.Errorf("QueryHash = %q, want hash of trimmed query", found.QueryHash)
	}
	if found.Query != "" {
		t.Errorf("raw Query must be empty by default, got %q", found.Query)
	}
	if len(found.Channels) == 0 {
		t.Error("Channels must report the active set")
	}
	if found.DurationMS < 0 {
		t.Errorf("DurationMS = %d", found.DurationMS)
	}
}

// TestEventSinkReceivesSearchExpansion: the sink sees search-expansion
// spend too (F-049), via the same fan-out as compile.
func TestEventSinkReceivesSearchExpansion(t *testing.T) {
	sink := &captureSink{}
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Direct fan-out assertion (the wiring point): the workspace recorder
	// multiplies file + sink.
	rec := w.usageRecorder()
	rec.RecordUsage(context.Background(), llm.UsageEvent{Pass: "expand", Provider: "openai", Model: "gpt-4o", Tier: -1, InputTokens: 5, OutputTokens: 2})
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %+v", sink.events)
	}
	usage, ok := sink.events[0].Data.(events.Usage)
	if !ok || usage.Pass != "expand" {
		t.Fatalf("sink events = %+v", sink.events)
	}
}

// TestSearchRawQueriesOptIn: raw query text enters the stream ONLY under
// the events.raw_queries opt-in (SPEC-07 privacy default).
func TestSearchRawQueriesOptIn(t *testing.T) {
	search := func(t *testing.T, cfgExtra string) events.SearchPerformed {
		t.Helper()
		dir := initWorkspaceWithConfig(t, cfgExtra)
		sink := &captureSink{}
		w, err := Open(context.Background(), dir, WithEventSink(sink))
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if _, err := w.Search(context.Background(), SearchRequest{Query: "attention mechanism"}); err != nil {
			t.Fatal(err)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, ev := range sink.events {
			if ev.Type == events.TypeSearchPerformed {
				return ev.Data.(events.SearchPerformed)
			}
		}
		t.Fatal("no search_performed event")
		return events.SearchPerformed{}
	}

	t.Run("default hashed", func(t *testing.T) {
		p := search(t, "")
		if p.Query != "" {
			t.Errorf("raw Query must be empty by default, got %q", p.Query)
		}
		if p.QueryHash == "" {
			t.Error("QueryHash must always be set")
		}
	})
	t.Run("opt-in raw", func(t *testing.T) {
		p := search(t, "\nevents:\n  raw_queries: true\n")
		if p.Query != "attention mechanism" {
			t.Errorf("raw Query = %q, want the opted-in query text", p.Query)
		}
	})
}

// TestWithEventSinkTypedNilDegradesEventFree (QA round 3): under
// events.enable=false the serve wiring passes a nil *Bus through
// WithEventSink — the workspace must degrade event-free, not panic on
// the first Emit.
func TestWithEventSinkTypedNilDegradesEventFree(t *testing.T) {
	dir := initWorkspace(t)
	var nilBus *events.Bus
	w, err := Open(context.Background(), dir, WithEventSink(nilBus))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.opts.sink != nil {
		t.Fatal("typed-nil sink must normalize to plain nil")
	}
	// The emit sites must no-op, not panic.
	if _, err := w.Search(context.Background(), SearchRequest{Query: "anything"}); err != nil {
		t.Fatalf("Search with normalized-nil sink: %v", err)
	}
	if _, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("# Nil sink\n\nContent.")}); err != nil {
		t.Fatalf("Capture with normalized-nil sink: %v", err)
	}
}
