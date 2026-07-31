package query

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
)

// guardProject builds a project dir (config pointed at the stub LLM) plus
// a shared DB seeded with searchable content — without it, buildQueryContext
// returns empty and Query short-circuits before any LLM call (edge case 8).
func guardProject(t *testing.T, srv *httptest.Server) (string, *storage.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	cfg := fmt.Sprintf(`version: 1
project: test
api:
  provider: openai
  api_key: sk-test
  base_url: %s
compiler:
  auto_commit: false
`, srv.URL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	// The doc-level context builder reads article files from disk
	// (query.go:485) — the fixture's entry must be backed by a real file or
	// the context assembles empty and Query short-circuits.
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wiki", "concepts", "attention.md"),
		[]byte("---\ntitle: Attention\n---\n# Attention\n\nSelf-attention computes pairwise token affinities.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	ms := memory.NewStore(db)
	if err := ms.Add(memory.Entry{
		ID: "concept:attention", Content: "Self-attention computes pairwise token affinities.",
		ArticlePath: "wiki/concepts/attention.md",
	}); err != nil {
		t.Fatal(err)
	}
	return dir, db, func() { db.Close() }
}

func emptyLLMStub(t *testing.T, content, finishReason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}, "finish_reason": finishReason}},
			"model":   "m",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func outputsExist(t *testing.T, dir string) bool {
	t.Helper()
	for _, sub := range []string{"outputs", "under_review"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "wiki", sub, "*.md"))
		if len(matches) > 0 {
			return true
		}
	}
	return false
}

// Empty content with finish_reason=length → error carries the actionable
// EmptyContentDetails hint, and NO file is written.
func TestQuery_EmptyContentLengthFinishReasonGuarded(t *testing.T) {
	srv := emptyLLMStub(t, "", "length")
	dir, db, cleanup := guardProject(t, srv)
	defer cleanup()

	_, err := Query(dir, "what is attention", "markdown", 5, QueryOpts{DB: db})
	if err == nil {
		t.Fatal("expected error for empty synthesis, got nil")
	}
	if !strings.Contains(err.Error(), "finish_reason") && !strings.Contains(err.Error(), "length") {
		t.Errorf("error lacks the actionable hint (finish_reason/length): %v", err)
	}
	if outputsExist(t, dir) {
		t.Error("a file was filed despite empty synthesis")
	}
}

// Bare empty content (no finish_reason) → error identifies the empty
// content; no hint (client.go:77-94 appends it only for length/reasoning).
func TestQuery_EmptyContentBareGuarded(t *testing.T) {
	srv := emptyLLMStub(t, "", "stop")
	dir, db, cleanup := guardProject(t, srv)
	defer cleanup()

	_, err := Query(dir, "what is attention", "markdown", 5, QueryOpts{DB: db})
	if err == nil {
		t.Fatal("expected error for empty synthesis, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty") {
		t.Errorf("error does not identify empty content: %v", err)
	}
	if outputsExist(t, dir) {
		t.Error("a file was filed despite empty synthesis")
	}
}

// Whitespace-only content errors and files nothing. (In practice the
// provider adapter normalizes whitespace to "" so EmptyContentDetails
// fires; the spec's fixed fallback covers any adapter that doesn't.)
func TestQuery_WhitespaceContentFallbackMessage(t *testing.T) {
	srv := emptyLLMStub(t, "  \n \t", "stop")
	dir, db, cleanup := guardProject(t, srv)
	defer cleanup()

	_, err := Query(dir, "what is attention", "markdown", 5, QueryOpts{DB: db})
	if err == nil {
		t.Fatal("expected error for whitespace synthesis, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty") {
		t.Errorf("error does not identify empty content: %v", err)
	}
	if outputsExist(t, dir) {
		t.Error("a file was filed despite whitespace synthesis")
	}
}
