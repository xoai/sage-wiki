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
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
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

func TestHandleRemovedSources_SingleSourcePrune(t *testing.T) {
	dir := t.TempDir()

	// Create article file on disk
	conceptDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptDir, 0755)
	articlePath := filepath.Join("wiki", "concepts", "attention.md")
	os.WriteFile(filepath.Join(dir, articlePath), []byte("# Attention\nContent."), 0644)

	// Set up DB with stores
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil)

	// Index the article in FTS5 and ontology
	memStore.Add(memory.Entry{ID: "concept:attention", Content: "Attention content", Tags: []string{"concept"}, ArticlePath: articlePath})
	ontStore.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention", ArticlePath: articlePath})

	// Set up manifest
	mf := manifest.New()
	mf.AddSource("raw/paper.pdf", "sha256:abc", "paper", 5000)
	mf.AddConcept("attention", articlePath, []string{"raw/paper.pdf"})

	// Execute cascade with prune=true
	handleRemovedSources(dir, []string{"raw/paper.pdf"}, mf, memStore, vecStore, ontStore, true)

	// Verify: article file deleted from disk
	if _, err := os.Stat(filepath.Join(dir, articlePath)); !os.IsNotExist(err) {
		t.Error("expected article file to be deleted")
	}

	// Verify: FTS5 entry removed
	results, _ := memStore.Search("attention", nil, 10)
	if len(results) != 0 {
		t.Errorf("expected 0 FTS5 results after prune, got %d", len(results))
	}

	// Verify: ontology entity removed
	e, _ := ontStore.GetEntity("attention")
	if e != nil {
		t.Error("expected ontology entity to be deleted")
	}

	// Verify: manifest cleaned
	if mf.ConceptCount() != 0 {
		t.Errorf("expected 0 concepts in manifest, got %d", mf.ConceptCount())
	}
	if mf.SourceCount() != 0 {
		t.Errorf("expected 0 sources in manifest, got %d", mf.SourceCount())
	}
}

func TestHandleRemovedSources_SingleSourceNoPrune(t *testing.T) {
	dir := t.TempDir()

	// Create article file
	conceptDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptDir, 0755)
	articlePath := filepath.Join("wiki", "concepts", "attention.md")
	os.WriteFile(filepath.Join(dir, articlePath), []byte("# Attention"), 0644)

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil)

	memStore.Add(memory.Entry{ID: "concept:attention", Content: "Attention", Tags: []string{"concept"}, ArticlePath: articlePath})
	ontStore.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention", ArticlePath: articlePath})

	mf := manifest.New()
	mf.AddSource("raw/paper.pdf", "sha256:abc", "paper", 5000)
	mf.AddConcept("attention", articlePath, []string{"raw/paper.pdf"})

	// Execute cascade with prune=false (warn only)
	handleRemovedSources(dir, []string{"raw/paper.pdf"}, mf, memStore, vecStore, ontStore, false)

	// Verify: article file still exists (no prune)
	if _, err := os.Stat(filepath.Join(dir, articlePath)); os.IsNotExist(err) {
		t.Error("article file should NOT be deleted without --prune")
	}

	// Verify: FTS5 entry still exists
	results, _ := memStore.Search("attention", nil, 10)
	if len(results) != 1 {
		t.Errorf("expected 1 FTS5 result (not pruned), got %d", len(results))
	}

	// Verify: concept still in manifest (orphaned but not deleted)
	if mf.ConceptCount() != 1 {
		t.Errorf("expected 1 concept (orphaned, not pruned), got %d", mf.ConceptCount())
	}

	// Verify: source removed from manifest
	if mf.SourceCount() != 0 {
		t.Errorf("expected 0 sources, got %d", mf.SourceCount())
	}
}

func TestHandleRemovedSources_MultiSource(t *testing.T) {
	dir := t.TempDir()

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil)

	mf := manifest.New()
	mf.AddSource("raw/paper.pdf", "sha256:abc", "paper", 5000)
	mf.AddSource("raw/notes.md", "sha256:def", "article", 1000)
	mf.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf", "raw/notes.md"})

	// Remove one source — concept should survive with updated sources
	handleRemovedSources(dir, []string{"raw/paper.pdf"}, mf, memStore, vecStore, ontStore, true)

	if mf.ConceptCount() != 1 {
		t.Fatalf("expected 1 concept (survived), got %d", mf.ConceptCount())
	}
	c := mf.Concepts["attention"]
	if len(c.Sources) != 1 || c.Sources[0] != "raw/notes.md" {
		t.Errorf("expected sources [raw/notes.md], got %v", c.Sources)
	}
	if mf.SourceCount() != 1 {
		t.Errorf("expected 1 source remaining, got %d", mf.SourceCount())
	}
}

func TestHandleRemovedSources_NoOrphans(t *testing.T) {
	dir := t.TempDir()

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil)

	mf := manifest.New()
	mf.AddSource("raw/paper.pdf", "sha256:abc", "paper", 5000)
	mf.AddConcept("lstm", "wiki/concepts/lstm.md", []string{"raw/other.pdf"})

	// Remove a source that doesn't affect any concept
	handleRemovedSources(dir, []string{"raw/paper.pdf"}, mf, memStore, vecStore, ontStore, true)

	if mf.ConceptCount() != 1 {
		t.Errorf("expected 1 concept unchanged, got %d", mf.ConceptCount())
	}
	if mf.SourceCount() != 0 {
		t.Errorf("expected 0 sources (removed), got %d", mf.SourceCount())
	}
}

func TestExtractFields(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		fields     []string
		wantFields map[string]string
		wantBody   string
	}{
		{
			name:       "confidence only",
			in:         "# Article\n\nContent here.\n\nConfidence: high",
			fields:     nil,
			wantFields: map[string]string{"confidence": "high"},
			wantBody:   "# Article\n\nContent here.",
		},
		{
			name:       "confidence with bold markdown",
			in:         "# Article\n\n**Confidence:** low",
			fields:     nil,
			wantFields: map[string]string{"confidence": "low"},
			wantBody:   "# Article",
		},
		{
			name:       "no confidence defaults to medium",
			in:         "# Article\n\nJust content.",
			fields:     nil,
			wantFields: map[string]string{"confidence": "medium"},
			wantBody:   "# Article\n\nJust content.",
		},
		{
			name:       "custom fields extracted",
			in:         "Content.\n\nLanguage: English\nDomain: machine learning\nConfidence: high",
			fields:     []string{"language", "domain"},
			wantFields: map[string]string{"confidence": "high", "language": "English", "domain": "machine learning"},
			wantBody:   "Content.",
		},
		{
			name:       "custom field missing gets omitted",
			in:         "Content.\n\nConfidence: low",
			fields:     []string{"language"},
			wantFields: map[string]string{"confidence": "low"},
			wantBody:   "Content.",
		},
		{
			name:       "confidence with numeric value normalized",
			in:         "Content.\n\nConfidence: 4/5",
			fields:     nil,
			wantFields: map[string]string{"confidence": "medium"},
			wantBody:   "Content.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, body := extractFields(tt.in, tt.fields)
			for k, want := range tt.wantFields {
				if got := fields[k]; got != want {
					t.Errorf("fields[%q] = %q, want %q", k, got, want)
				}
			}
			// Check no unexpected fields (except confidence which always exists)
			for k := range fields {
				if _, expected := tt.wantFields[k]; !expected {
					t.Errorf("unexpected field %q = %q", k, fields[k])
				}
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestStripLLMFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no frontmatter",
			in:   "# Article\n\nContent here.",
			want: "# Article\n\nContent here.",
		},
		{
			name: "bare frontmatter",
			in:   "---\nconcept: test\nconfidence: high\n---\n\n# Article\n\nContent.",
			want: "# Article\n\nContent.",
		},
		{
			name: "code-fenced frontmatter",
			in:   "```yaml\n---\nconcept: test\nconfidence: high\n---\n```\n\n# Article\n\nContent.",
			want: "# Article\n\nContent.",
		},
		{
			name: "code-fenced then bare",
			in:   "```yaml\n---\nconcept: test\n---\n```\n---\nconcept: test2\n---\n\nContent.",
			want: "Content.",
		},
		{
			name: "empty after strip",
			in:   "---\nconcept: test\n---",
			want: "",
		},
		{
			name: "content before frontmatter preserved",
			in:   "Here is the article:\n---\nconcept: test\n---\n\nContent.",
			want: "Here is the article:\n---\nconcept: test\n---\n\nContent.",
		},
		{
			name: "horizontal rule in body preserved",
			in:   "---\nconcept: test\n---\n\n# Heading\n\nParagraph\n\n---\n\nMore content.",
			want: "# Heading\n\nParagraph\n\n---\n\nMore content.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripLLMFrontmatter(tt.in)
			if got != tt.want {
				t.Errorf("stripLLMFrontmatter() = %q, want %q", got, tt.want)
			}
		})
	}
}
