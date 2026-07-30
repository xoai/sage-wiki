package compiler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// Plan T3: a source-less concept makes ZERO article-write LLM calls (gate
// runs before WriteArticles), while a sourced one proceeds.
func TestCompileGatesSourcelessConceptNoLLM(t *testing.T) {
	var writeCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}
		var content string
		switch {
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name": "rap", "aliases": [], "sources": [], "type": "concept"},
			             {"name": "real-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			writeCalls.Add(1)
			content = "---\nconcept: real-concept\n---\n\n# Real\n\nA real article."
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject matter at sufficient length for validation.\n\n## Concepts\n\nreal-concept: A real one."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")
	cfgContent := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + srv.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Topic\n\nContent about the topic."), 0644)

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if got := writeCalls.Load(); got != 1 {
		t.Errorf("article-write LLM calls = %d, want exactly 1 (rap gated out)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "rap.md")); !os.IsNotExist(err) {
		t.Error("source-less concept got an article file")
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "real-concept.md")); err != nil {
		t.Error("sourced concept lost its article")
	}
}
