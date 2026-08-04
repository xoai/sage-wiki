package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/pkg/events"
)

// skipStub serves summarize/extract/write/embed for one doc — enough for a
// tier-3 doc to COMPLETE (embed must succeed or tierComplete never fires).
func skipStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		if body["input"] != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0}},
			})
			return
		}

		messages, _ := body["messages"].([]any)
		var all string
		for _, m := range messages {
			if mm, ok := m.(map[string]any); ok {
				c, _ := mm["content"].(string)
				all += c
			}
		}
		var content string
		switch {
		case strings.Contains(all, "concept extraction system"):
			content = `[{"name": "alpha-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case strings.Contains(all, "wiki author writing a comprehensive article"):
			content = "# Alpha Concept\n\nEngine skip test article with enough content to pass validation checks.\n\n## See also\n\n[[alpha-concept]]"
		default:
			content = "## Key claims\n\nEngine skip test summary with sufficient length to pass the quality gate.\n\n## Concepts\n\nalpha-concept: A concept."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 60},
		})
	}))
}

func writeSkipConfig(t *testing.T, dir, serverURL string) {
	t.Helper()
	cfg := `version: 1
project: ws
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + serverURL + `
models:
  summarize: gpt-4o-mini
compiler:
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCompile_SkippedAndAdoptedCrossEngine pins the SPEC-04 engine mirror:
// Skipped/Adopted cross w.Compile → CompileResult (F-review B-3).
func TestCompile_SkippedAndAdoptedCrossEngine(t *testing.T) {
	srv := skipStub(t)
	defer srv.Close()
	dir := initWorkspace(t)
	writeSkipConfig(t, dir, srv.URL)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Alpha\n\nAlpha content for skip testing."), 0o644)

	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	res1, err := w.Compile(context.Background(), CompileRequest{})
	if err != nil {
		t.Fatalf("compile 1: %v", err)
	}
	if len(res1.Skipped) != 0 {
		t.Errorf("compile 1 Skipped = %v, want 0 (fresh workspace)", res1.Skipped)
	}

	res2, err := w.Compile(context.Background(), CompileRequest{})
	if err != nil {
		t.Fatalf("compile 2: %v", err)
	}
	if len(res2.Skipped) != 1 || res2.Skipped[0].Reason != "unchanged" {
		t.Errorf("compile 2 Skipped = %+v, want 1 unchanged doc", res2.Skipped)
	}

	res3, err := w.Compile(context.Background(), CompileRequest{Force: true})
	if err != nil {
		t.Fatalf("compile 3 (force): %v", err)
	}
	if len(res3.Skipped) != 0 {
		t.Errorf("force Skipped = %+v, want 0", res3.Skipped)
	}
}

// TestCompile_SkipEventEmitted pins the compile_skip event: one per skipped
// doc, through the installed sink (spec §Observability).
func TestCompile_SkipEventEmitted(t *testing.T) {
	srv := skipStub(t)
	defer srv.Close()
	dir := initWorkspace(t)
	writeSkipConfig(t, dir, srv.URL)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Alpha\n\nAlpha content."), 0o644)

	sink := &collectSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Compile(context.Background(), CompileRequest{}); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	skipBefore := len(sink.kinds[events.TypeCompileSkip])
	sink.mu.Unlock()
	if skipBefore != 0 {
		t.Fatalf("compile 1 emitted %d skip events, want 0", skipBefore)
	}

	if _, err := w.Compile(context.Background(), CompileRequest{}); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	got := sink.kinds[events.TypeCompileSkip]
	sink.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("compile 2 emitted %d skip events, want 1", len(got))
	}
	ev := got[0]
	skip, ok := ev.Data.(events.CompileSkip)
	if !ok {
		t.Fatalf("event data = %T, want events.CompileSkip", ev.Data)
	}
	if ev.Workspace != filepath.Base(dir) || skip.DocID != "raw/a.md" || skip.Reason != "unchanged" {
		t.Errorf("event = %+v data = %+v, want workspace=%s doc_id=raw/a.md reason=unchanged", ev, skip, filepath.Base(dir))
	}
}

// TestExplainCompile_Golden pins AC-5: the explanation carries every
// documented component field and the right verdict per state.
func TestExplainCompile_Golden(t *testing.T) {
	srv := skipStub(t)
	defer srv.Close()
	dir := initWorkspace(t)
	writeSkipConfig(t, dir, srv.URL)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Alpha\n\nAlpha content."), 0o644)

	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Never compiled: verdict content (new).
	ex, err := w.ExplainCompile(context.Background(), "raw/a.md")
	if err != nil {
		t.Fatalf("explain (new): %v", err)
	}
	if ex.Verdict != "compile: content (new)" {
		t.Errorf("new-doc verdict = %q, want compile: content (new)", ex.Verdict)
	}
	if ex.Pipeline == "" || ex.Templates == "" || ex.Models == "" || ex.ConfigHash == "" || ex.Embed == "" || ex.Key == "" {
		t.Errorf("explanation missing components: %+v", ex)
	}
	if !strings.Contains(ex.Templates, "write_article@") {
		t.Errorf("templates component missing versioned entries: %q", ex.Templates)
	}

	if _, err := w.Compile(context.Background(), CompileRequest{}); err != nil {
		t.Fatal(err)
	}

	// Compiled: verdict skip unchanged, stored key matches computed.
	ex2, err := w.ExplainCompile(context.Background(), "raw/a.md")
	if err != nil {
		t.Fatalf("explain (compiled): %v", err)
	}
	if ex2.Verdict != "skip: unchanged" {
		t.Errorf("compiled verdict = %q, want skip: unchanged", ex2.Verdict)
	}
	if ex2.StoredKey == "" || ex2.StoredKey != ex2.Key {
		t.Errorf("stored %q vs computed %q — want equal", ex2.StoredKey, ex2.Key)
	}
}

type collectSink struct {
	mu    sync.Mutex
	kinds map[events.Type][]events.Event
}

func (s *collectSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kinds == nil {
		s.kinds = map[events.Type][]events.Event{}
	}
	s.kinds[ev.Type] = append(s.kinds[ev.Type], ev)
}

var _ = fmt.Sprintf
