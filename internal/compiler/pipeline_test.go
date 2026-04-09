package compiler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func TestCompileDryRun(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Add source files
	os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Test A\nContent A."), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "b.md"), []byte("# Test B\nContent B."), 0644)

	result, err := Compile(dir, CompileOpts{DryRun: true})
	if err != nil {
		t.Fatalf("Compile dry run: %v", err)
	}

	if result.Added != 2 {
		t.Errorf("expected 2 added, got %d", result.Added)
	}
	if result.Summarized != 0 {
		t.Error("dry run should not produce summaries")
	}
}

func TestCompileNothingToDo(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	result, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Added != 0 {
		t.Error("expected nothing to compile")
	}
}

func TestCompileWithMockLLM(t *testing.T) {
	callCount := 0
	// Mock LLM server — returns different responses for different passes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
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
		if strings.Contains(lastMsg, "concept extraction system") {
			// Pass 2: concept extraction — return JSON
			content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/article1.md"], "type": "concept"}]`
		} else if strings.Contains(lastMsg, "wiki author writing a comprehensive article") {
			// Pass 3: article writing
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA test concept."
		} else {
			// Pass 1: summarization
			content = "## Key claims\nTest claim.\n\n## Concepts\ntest-concept"
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Update config to use mock server
	cfgContent := `
version: 1
project: test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0644)

	// Add source files
	os.WriteFile(filepath.Join(dir, "raw", "article1.md"), []byte("# Self-Attention\n\nSelf-attention computes contextual representations."), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "article2.md"), []byte("# Flash Attention\n\nOptimizes memory access for attention."), 0644)

	result, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if result.Summarized != 2 {
		t.Errorf("expected 2 summarized, got %d", result.Summarized)
	}

	// Verify summaries were written
	summaryDir := filepath.Join(dir, "wiki", "summaries")
	entries, _ := os.ReadDir(summaryDir)
	if len(entries) != 2 {
		t.Errorf("expected 2 summary files, got %d", len(entries))
	}

	// Verify concepts were extracted
	if result.ConceptsExtracted < 1 {
		t.Errorf("expected at least 1 concept, got %d", result.ConceptsExtracted)
	}

	// Verify manifest updated
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if mf.SourceCount() != 2 {
		t.Errorf("expected 2 sources in manifest, got %d", mf.SourceCount())
	}

	// Verify CHANGELOG written
	changelogPath := filepath.Join(dir, "wiki", "CHANGELOG.md")
	if _, err := os.Stat(changelogPath); os.IsNotExist(err) {
		t.Error("CHANGELOG.md should exist")
	}
}

func TestCompileCheckpointResume(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Create a checkpoint with article2 already completed
	statePath := filepath.Join(dir, ".sage", "compile-state.json")
	state := CompileState{
		CompileID: "test",
		StartedAt: "2026-04-04T00:00:00Z",
		Pass:      1,
		Completed: []string{"raw/article1.md"},
		Pending:   []string{"raw/article2.md"},
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	os.WriteFile(statePath, data, 0644)

	// Verify checkpoint can be loaded
	loaded, err := loadCompileState(statePath)
	if err != nil {
		t.Fatalf("loadCompileState: %v", err)
	}
	if len(loaded.Completed) != 1 {
		t.Errorf("expected 1 completed, got %d", len(loaded.Completed))
	}
	if len(loaded.Pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(loaded.Pending))
	}
}

func TestCompileStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	state := &CompileState{
		CompileID: "20260404-120000",
		StartedAt: "2026-04-04T12:00:00Z",
		Pass:      1,
		Completed: []string{"a.md", "b.md"},
		Pending:   []string{"c.md"},
		Failed:    []FailedSource{{Path: "d.md", Error: "rate limited", Attempts: 3}},
	}

	if err := saveCompileState(path, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadCompileState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.CompileID != state.CompileID {
		t.Errorf("compile_id mismatch")
	}
	if len(loaded.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(loaded.Failed))
	}
}
