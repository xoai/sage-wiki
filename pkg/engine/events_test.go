package engine

import (
	"context"
	"os"
	"path/filepath"
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
	ev := sink.events[0]
	if ev.Kind != events.KindUsage || ev.Provider != "openai" || ev.Model != "gpt-4o-mini" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Tier != 3 {
		t.Errorf("Tier = %d, want 3 (compile-scoped)", ev.Tier)
	}
	if ev.Cost == nil {
		t.Error("gpt-4o-mini is priced — Cost must be set")
	}
	if ev.Workspace != dir {
		t.Errorf("Workspace = %q, want %q", ev.Workspace, dir)
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
	if len(sink.events) != 1 || sink.events[0].Pass != "expand" {
		t.Fatalf("sink events = %+v", sink.events)
	}
}
