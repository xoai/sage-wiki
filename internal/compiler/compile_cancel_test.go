package compiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestCompile_CancelledSourceResumes proves the P1-1 contract end-to-end: a
// compile cancelled mid-summarize returns promptly, does NOT count the cancelled
// source as an error or mark it compiled, and a subsequent compile reprocesses it
// to completion. Cancelled != failed.
func TestCompile_CancelledSourceResumes(t *testing.T) {
	var blocking atomic.Bool
	blocking.Store(true)
	started := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		// Scan ALL messages — the pass-identifying phrases live in the SYSTEM
		// message, not the trailing user prompt.
		var all strings.Builder
		for _, mm := range messages {
			if m, ok := mm.(map[string]any); ok {
				if c, ok := m["content"].(string); ok {
					all.WriteString(c)
					all.WriteByte(' ')
				}
			}
		}
		msg := all.String()

		isConcept := strings.Contains(msg, "concept extraction system")
		isArticle := strings.Contains(msg, "wiki author writing comprehensive")
		isSummarize := !isConcept && !isArticle

		// On the first compile, block the summarize call until the request's
		// context is cancelled — simulating a long in-flight LLM call the user
		// Ctrl-Cs. req.WithContext propagates the client cancel to r.Context().
		if isSummarize && blocking.Load() {
			once.Do(func() { close(started) })
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
			}
		}

		var content string
		switch {
		case isConcept:
			content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case isArticle:
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA sufficiently long test concept article body for validation."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject at length.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
	cfg := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"),
		[]byte("# Attention\n\nSelf-attention computes contextual representations of tokens."), 0644)

	// First compile — cancel while blocked on the summarize call.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *CompileResult, 1)
	go func() {
		r, _ := Compile(dir, CompileOpts{Ctx: ctx})
		done <- r
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("summarize call never reached the server")
	}
	cancel()

	var r1 *CompileResult
	select {
	case r1 = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("compile did not return promptly after cancel")
	}
	if r1 != nil && r1.Summarized != 0 {
		t.Errorf("cancelled compile summarized %d, want 0", r1.Summarized)
	}
	if r1 != nil && r1.Errors != 0 {
		t.Errorf("cancelled source counted as %d errors; cancelled must not be failure", r1.Errors)
	}

	// Second compile — no cancel, server responds normally. The cancelled source
	// must be reprocessed (it was never marked compiled, never added to Failed).
	blocking.Store(false)
	r2, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	if r2.Summarized != 1 {
		t.Errorf("resume summarized %d, want 1 — cancelled source was not reprocessed", r2.Summarized)
	}
	// Resume runs to completion, not just Pass 1.
	if r2.ConceptsExtracted < 1 {
		t.Errorf("resume extracted %d concepts, want >= 1", r2.ConceptsExtracted)
	}
}
