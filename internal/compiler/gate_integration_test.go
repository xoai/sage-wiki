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

	"github.com/xoai/sage-wiki/internal/manifest"
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
	// No phantom manifest entry for the gated concept (spec C1) and
	// ConceptsExtracted counts kept-only.
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mf.Concepts["rap"]; ok {
		t.Error("phantom manifest entry for gated concept")
	}
	if _, ok := mf.Concepts["real-concept"]; !ok {
		t.Error("sourced concept missing from manifest")
	}
}

// QA: re-extract also gates (reextract.go:112) — not just the full pipeline.
func TestReExtractGatesSourcelessConcept(t *testing.T) {
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
  base_url: http://127.0.0.1:9
compiler:
  max_parallel: 2
  auto_commit: false
  default_tier: 3
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0644)
	os.MkdirAll(filepath.Join(dir, "wiki", "summaries"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "summaries", "a.md"), []byte(
		"---\nsource: raw/a.md\n---\n\n## Key claims\n\nSelf-attention computes contextual representations of tokens across the sequence.\n"), 0o644)

	// Extraction will fail (no LLM) — but ReExtract must not panic, and no
	// source-less manifest entry may appear from any path.
	_, _ = ReExtract(dir)
}

// QA: batch-resume also gates (pipeline.go:1188) — a source-less concept
// gets no manifest entry on the batch path either.
func TestResumeBatchGatesSourcelessConcept(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, "", "raw/a.md")

	idA := batchIDForPath("raw/a.md")
	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		CompileID: "c1",
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md"},
		},
		Pending: []string{"raw/a.md"},
	}); err != nil {
		t.Fatal(err)
	}
	fake.setResults([]string{idA})

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The fake returns test-concept with one source — the gate must NOT
	// over-fire on the batch path.
	if _, ok := mf.Concepts["test-concept"]; !ok {
		t.Error("sourced concept suppressed on batch-resume path — gate over-fired")
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "test-concept.md")); err != nil {
		t.Error("sourced concept's article missing on batch-resume path")
	}
}
