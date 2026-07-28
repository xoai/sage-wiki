package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/web"
)

// countingLLM serves OpenAI chat completions and counts every call, so a test
// can assert an entry point made none.
func countingLLM(t *testing.T, calls *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(calls, 1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `["wiki search rank"]`}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeDefaultsOffConfig points the LLM at the counting server and the
// embedder at the fake one, leaving every LLM-stage knob unset.
func writeDefaultsOffConfig(t *testing.T, dir, llmURL, embedURL string) {
	t.Helper()
	cfg := fmt.Sprintf(`version: 1
project: defaults
output: wiki
sources:
  - path: notes
    type: md
api:
  provider: openai
  api_key: test-key
  base_url: %s
models:
  summarize: m
  extract: m
  write: m
  lint: m
  query: m
embed:
  provider: openai
  model: fake-embed
  dimensions: 8
  api_key: test-key
  base_url: %s
search:
  hybrid_weight_bm25: 0.7
  hybrid_weight_vector: 0.3
trust:
  include_outputs: "true"
`, llmURL, embedURL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
}

// V-M5b: the LLM stages (query expansion, reranking) are OFF by default at
// every entry point. They cost a call per query and change ranking, so they
// must be opted into per call — never inherited from a Q&A-shaped default.
func TestEntryPointLLMStagesDefaultOff(t *testing.T) {
	dir := setupParityProject(t)
	embedSrv := fakeEmbedServer(t)
	var calls int64
	llmSrv := countingLLM(t, &calls)
	writeDefaultsOffConfig(t, dir, llmSrv.URL, embedSrv.URL)

	const query = "wiki search rank"

	t.Run("cli", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		if got := cliDocs(t, dir, query, 10, nil); len(got) == 0 {
			t.Fatal("CLI returned no results")
		}
		if n := atomic.LoadInt64(&calls); n != 0 {
			t.Errorf("CLI made %d LLM calls with no --expand/--rerank, want 0", n)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		if got := mcpDocs(t, dir, query, 10, nil); len(got) == 0 {
			t.Fatal("MCP returned no results")
		}
		if n := atomic.LoadInt64(&calls); n != 0 {
			t.Errorf("MCP made %d LLM calls with expand/rerank unset, want 0", n)
		}
	})

	t.Run("web", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		if got := webHits(t, dir, query, 10, nil); len(got) == 0 {
			t.Fatal("web returned no results")
		}
		if n := atomic.LoadInt64(&calls); n != 0 {
			t.Errorf("web made %d LLM calls, want 0 (the web surface has no opt-in)", n)
		}
	})

	// The counter is only evidence if it can go up: opting in on the CLI must
	// reach the LLM, or the three assertions above prove nothing.
	t.Run("cli --expand opts in", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		oldDir, oldFormat := projectDir, outputFormat
		projectDir, outputFormat = dir, "json"
		defer func() { projectDir, outputFormat = oldDir, oldFormat }()
		if err := searchCmd.Flags().Set("expand", "true"); err != nil {
			t.Fatal(err)
		}
		defer searchCmd.Flags().Set("expand", "false")

		r, w, _ := os.Pipe()
		oldStdout := os.Stdout
		os.Stdout = w
		err := runSearch(searchCmd, strings.Fields(query))
		w.Close()
		os.Stdout = oldStdout
		_, _ = io.ReadAll(r)
		if err != nil {
			t.Fatalf("runSearch --expand: %v", err)
		}
		if n := atomic.LoadInt64(&calls); n == 0 {
			t.Error("CLI --expand made no LLM call — the defaults-off assertions above are vacuous")
		}
	})

	// Same for MCP, whose opt-in is a tool argument rather than a flag.
	t.Run("mcp expand arg opts in", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		srv, err := wikimcp.NewServer(dir)
		if err != nil {
			t.Fatalf("mcp.NewServer: %v", err)
		}
		defer srv.Close()
		req := mcpgo.CallToolRequest{}
		req.Params.Arguments = map[string]any{"query": query, "limit": float64(10), "expand": true}
		res := srv.CallTool(context.Background(), "wiki_search", req)
		if res.IsError {
			t.Fatalf("mcp wiki_search expand: %+v", res.Content)
		}
		if n := atomic.LoadInt64(&calls); n == 0 {
			t.Error("MCP expand:true made no LLM call — the defaults-off assertion above is vacuous")
		}
	})

	// The web surface has no opt-in at all: an expand/rerank query parameter
	// must not turn the stages on.
	t.Run("web ignores expand param", func(t *testing.T) {
		atomic.StoreInt64(&calls, 0)
		srv, err := web.NewWebServer(dir)
		if err != nil {
			t.Fatalf("NewWebServer: %v", err)
		}
		defer srv.Close()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET",
			"http://127.0.0.1/api/search?q=wiki+search+rank&expand=true&rerank=true", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("web /api/search: %d %s", rec.Code, rec.Body.String())
		}
		if n := atomic.LoadInt64(&calls); n != 0 {
			t.Errorf("web made %d LLM calls for expand=true&rerank=true, want 0", n)
		}
	})
}
