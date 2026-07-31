package mcp

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// queryToolProject builds a project whose config routes LLM calls to the
// stub, seeded with a searchable entry AND its backing article file — the
// doc-level context builder reads files from disk, so both are required or
// Query short-circuits before the LLM (spec edge case 8).
func queryToolProject(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")
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
  base_url: ` + baseURL + `
models:
  summarize: gpt-4o-mini
  query: gpt-4o-mini
compiler:
  auto_commit: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func seedAttentionArticle(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "attention.md"),
		[]byte("---\ntitle: Attention\n---\n# Attention\n\nSelf-attention computes pairwise token affinities.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The doc-level search reads the FTS entry from the PROJECT's DB — seed
	// it before NewServer opens its handle.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := memory.NewStore(db).Add(memory.Entry{
		ID: "concept:attention", Content: "Self-attention computes pairwise token affinities.",
		ArticlePath: "wiki/concepts/attention.md",
	}); err != nil {
		t.Fatal(err)
	}
}

func queryLLMStub(t *testing.T, content string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "boom"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}, "finish_reason": "stop"}},
			"model":   "m",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newQueryToolServer(t *testing.T, dir string) *Server {
	t.Helper()
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// Q-01: wiki_query is registered as the 19th tool.
func TestWikiQueryRegistered(t *testing.T) {
	srv := newQueryToolServer(t, queryToolProject(t, "http://127.0.0.1:9"))
	tools := srv.MCPServer().ListTools()
	if len(tools) != 19 {
		t.Errorf("tool count = %d, want 19", len(tools))
	}
	if _, ok := tools["wiki_query"]; !ok {
		t.Error("wiki_query not in registry")
	}
}

// Q-02/Q-03: missing and blank question.
func TestWikiQueryQuestionRequired(t *testing.T) {
	srv := newQueryToolServer(t, queryToolProject(t, "http://127.0.0.1:9"))
	for _, args := range []map[string]any{{}, {"question": ""}, {"question": "   "}} {
		res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(args))
		if !res.IsError {
			t.Errorf("args %v: expected error result", args)
		}
	}
}

// Q-04: top_k bounds.
func TestWikiQueryTopKBounds(t *testing.T) {
	for _, k := range []any{0.9, 20.9, 21.0, -1.0} {
		srv := newQueryToolServer(t, queryToolProject(t, "http://127.0.0.1:9"))
		res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
			"question": "what is attention", "top_k": k,
		}))
		if !res.IsError {
			t.Errorf("top_k %v: expected range error", k)
		}
	}
}

// Q-04 (other half): top_k: 0 is treated as unset → the call proceeds with
// the default of 5 — it must NOT be a range error.
func TestWikiQueryTopKZeroDefaults(t *testing.T) {
	llm := queryLLMStub(t, "Attention lets tokens weigh each other [[attention]].", 200)
	dir := queryToolProject(t, llm.URL)
	seedAttentionArticle(t, dir)
	srv := newQueryToolServer(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention", "top_k": 0.0,
	}))
	if res.IsError {
		t.Fatalf("top_k 0 should default to 5, got error: %s", resultText(res))
	}
}

// Q-05: end-to-end with stubbed LLM — answer, sources, output_path, file.
func TestWikiQueryEndToEnd(t *testing.T) {
	llm := queryLLMStub(t, "Attention lets tokens weigh each other [[attention]].", 200)
	dir := queryToolProject(t, llm.URL)
	seedAttentionArticle(t, dir) // seed BEFORE NewServer opens its handle
	srv := newQueryToolServer(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	text := resultText(res)
	var out struct {
		Answer     string   `json:"answer"`
		Sources    []string `json:"sources"`
		OutputPath string   `json:"output_path"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, text)
	}
	if !strings.Contains(out.Answer, "Attention") {
		t.Errorf("answer missing: %q", out.Answer)
	}
	if out.OutputPath == "" {
		t.Fatal("output_path empty — nothing was filed")
	}
	// output_path is project-relative.
	absFile := filepath.Join(dir, out.OutputPath)
	if _, err := os.Stat(absFile); err != nil {
		t.Errorf("filed output missing on disk: %v", err)
	}
	// The filed file carries frontmatter and the answer body.
	body, err := os.ReadFile(absFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "---") {
		t.Error("filed output lacks frontmatter")
	}
	if !strings.Contains(string(body), "Attention lets tokens weigh each other") {
		t.Error("filed output lacks the answer body")
	}
	// Default config (include_outputs unset) files to under_review/.
	if !strings.Contains(out.OutputPath, "under_review") {
		t.Errorf("default config should file to under_review/, got %s", out.OutputPath)
	}
}

// Q-06: include_outputs "true" files to outputs/.
func TestWikiQueryIncludeOutputsTrue(t *testing.T) {
	llm := queryLLMStub(t, "Attention lets tokens weigh each other [[attention]].", 200)
	dir := queryToolProject(t, llm.URL)
	// Append trust mode to the generated config.
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append(raw, []byte("trust:\n  include_outputs: \"true\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	seedAttentionArticle(t, dir) // seed BEFORE NewServer opens its handle
	srv := newQueryToolServer(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	text := resultText(res)
	var out struct {
		OutputPath string `json:"output_path"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, text)
	}
	if !strings.Contains(out.OutputPath, filepath.Join("wiki", "outputs")) && !strings.Contains(out.OutputPath, "wiki/outputs") {
		t.Errorf("include_outputs true should file to outputs/, got %s", out.OutputPath)
	}
}

// Q-07: LLM 500 → error result, no file.
func TestWikiQueryLLMFailure(t *testing.T) {
	llm := queryLLMStub(t, "", 500)
	dir := queryToolProject(t, llm.URL)
	srv := newQueryToolServer(t, dir)
	seedAttentionArticle(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention",
	}))
	if !res.IsError {
		t.Fatal("expected error result for LLM 500")
	}
	if filed, _ := filepath.Glob(filepath.Join(dir, "wiki", "under_review", "*.md")); len(filed) > 0 {
		t.Errorf("a file was filed despite LLM failure: %v", filed)
	}
}

// Frontmatter escaping for hostile questions (YAML injection probe).
func TestWikiQueryFrontmatterEscaping(t *testing.T) {
	llm := queryLLMStub(t, "An answer about quoting [[attention]].", 200)
	dir := queryToolProject(t, llm.URL)
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append(raw, []byte("trust:\n  include_outputs: \"true\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	seedAttentionArticle(t, dir)
	srv := newQueryToolServer(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what does \"attention\" mean?\nformat: injected",
	}))
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	var out struct {
		OutputPath string `json:"output_path"`
	}
	json.Unmarshal([]byte(resultText(res)), &out)
	body, err := os.ReadFile(filepath.Join(dir, out.OutputPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "question: \"what does \"attention\" mean?") {
		t.Error("unescaped quotes corrupted the frontmatter")
	}
	if strings.Contains(string(body), "\nformat: injected\n") {
		t.Error("newline in question injected a frontmatter key")
	}
}

// Two different unicode-only questions on the same day must not clobber
// each other — slugify yields "" for both, dedup must suffix (Gate 8).
func TestWikiQueryUnicodeFilenameDedup(t *testing.T) {
	llm := queryLLMStub(t, "回答 [[attention]]。", 200)
	dir := queryToolProject(t, llm.URL)
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append(raw, []byte("trust:\n  include_outputs: \"true\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// CJK content containing the exact question strings — FTS5's unicode61
	// tokenizer treats a pure-CJK run as ONE token, so the entry must carry
	// the literal question text to match (else the empty-context
	// short-circuit fires and nothing files).
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "zhuyili.md"),
		[]byte("---\ntitle: 注意力机制\n---\n# 注意力机制\n\n什么是注意力机制 什么是变压器架构 — answers to both questions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db2, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.NewStore(db2).Add(memory.Entry{
		ID: "concept:zhuyili", Content: "什么是注意力机制 什么是变压器架构 — answers to both questions.",
		ArticlePath: "wiki/concepts/zhuyili.md",
	}); err != nil {
		t.Fatal(err)
	}
	db2.Close()
	srv := newQueryToolServer(t, dir)

	paths := map[string]bool{}
	for _, q := range []string{"什么是注意力机制", "什么是变压器架构"} {
		res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{"question": q}))
		if res.IsError {
			t.Fatalf("unexpected error: %+v", res)
		}
		var out struct {
			OutputPath string `json:"output_path"`
		}
		json.Unmarshal([]byte(resultText(res)), &out)
		if paths[out.OutputPath] {
			t.Errorf("two different questions filed to the same path %s — data loss", out.OutputPath)
		}
		paths[out.OutputPath] = true
	}
}

// A filing failure must surface as filing_error — success with an empty
// output_path would masquerade as the benign no-content short-circuit.
func TestWikiQueryFilingErrorSurfaces(t *testing.T) {
	llm := queryLLMStub(t, "An answer [[attention]].", 200)
	dir := queryToolProject(t, llm.URL)
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, append(raw, []byte("trust:\n  include_outputs: \"true\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	seedAttentionArticle(t, dir)
	// Make filing fail platform-independently: replace the outputs
	// DIRECTORY with a same-named FILE — MkdirAll/WriteFile under it fails
	// everywhere (chmod is a no-op on Windows, and a path blocker is
	// defeated by the dedup suffix).
	outputsDir := filepath.Join(dir, "wiki", "outputs")
	if err := os.RemoveAll(outputsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputsDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newQueryToolServer(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention",
	}))
	var out struct {
		OutputPath  string `json:"output_path"`
		FilingError string `json:"filing_error"`
	}
	json.Unmarshal([]byte(resultText(res)), &out)
	if out.FilingError == "" {
		t.Error("filing failure was swallowed — no filing_error in response")
	}
	if out.OutputPath != "" {
		t.Errorf("output_path should be empty on filing failure, got %s", out.OutputPath)
	}
}

// NaN top_k (in-process path only — JSON can't carry it) → range error.
func TestWikiQueryTopKNaN(t *testing.T) {
	srv := newQueryToolServer(t, queryToolProject(t, "http://127.0.0.1:9"))
	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "q", "top_k": math.NaN(),
	}))
	if !res.IsError {
		t.Error("NaN top_k should be a range error")
	}
}

// Q-07b: LLM 200 with empty content → guarded error, no file.
func TestWikiQueryEmptyContentGuarded(t *testing.T) {
	llm := queryLLMStub(t, "", 200)
	dir := queryToolProject(t, llm.URL)
	srv := newQueryToolServer(t, dir)
	seedAttentionArticle(t, dir)

	res := srv.CallTool(context.Background(), "wiki_query", makeToolRequest(map[string]any{
		"question": "what is attention",
	}))
	if !res.IsError {
		t.Fatal("expected error result for empty synthesis")
	}
	text := resultText(res)
	if !strings.Contains(strings.ToLower(text), "empty") {
		t.Errorf("error does not identify empty content: %s", text)
	}
}
