package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
)

// SPEC-08 Task 9: MCP tool-layer hardening — capture batch cap, empty-slug
// rejection, encoding gate, concept charset, limit_exceeded emission.

type mcpCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *mcpCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *mcpCaptureSink) count(ty events.Type) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events {
		if ev.Type == ty {
			n++
		}
	}
	return n
}

// captureProject builds a greenfield workspace wired to a stub LLM that
// returns `items` as the capture-extraction result.
func captureProject(t *testing.T, itemsJSON string, extraLimits string) string {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": itemsJSON,
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(fake.Close)

	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "mcphardening", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg := `version: 1
project: mcphardening
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai-compatible
  api_key: sk-test
  base_url: ` + fake.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  auto_commit: false
` + extraLimits
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func callCapture(srv *Server, content string) *mcplib.CallToolResult {
	return srv.CallTool(context.Background(), "wiki_capture", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_capture",
			Arguments: map[string]any{"content": content},
		},
	})
}

func captureFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "raw", "captures"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func TestMCPCaptureBatchCapEnforced(t *testing.T) {
	items := `[{"title":"One","content":"a"},{"title":"Two","content":"b"},{"title":"Three","content":"c"}]`
	dir := captureProject(t, items, "limits:\n  max_docs_per_capture_batch: 2\n")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	sink := &mcpCaptureSink{}
	srv.SetEventSink(sink)

	res := callCapture(srv, "capture three items please")
	if !res.IsError {
		t.Fatal("capture over the batch cap must fail")
	}
	text := res.Content[0].(mcplib.TextContent).Text
	if !strings.Contains(text, "capture_batch") && !strings.Contains(text, "limit") {
		t.Errorf("error text does not name the limit: %q", text)
	}
	if got := captureFiles(t, dir); got != 0 {
		t.Errorf("over-cap capture persisted %d files", got)
	}
	if sink.count(events.TypeLimitExceeded) != 1 {
		t.Errorf("limit_exceeded events = %d, want 1", sink.count(events.TypeLimitExceeded))
	}
}

func TestMCPCaptureBatchUnderCapWorks(t *testing.T) {
	items := `[{"title":"One","content":"a"},{"title":"Two","content":"b"}]`
	dir := captureProject(t, items, "limits:\n  max_docs_per_capture_batch: 3\n")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	res := callCapture(srv, "capture two items please")
	if res.IsError {
		t.Fatalf("under-cap capture failed: %s", res.Content[0].(mcplib.TextContent).Text)
	}
	if got := captureFiles(t, dir); got != 2 {
		t.Errorf("captured files = %d, want 2", got)
	}
}

func TestMCPCaptureEmptySlugRejected(t *testing.T) {
	// A title that slugifies to empty must fail the capture (behavior
	// change 6 — the old code fell back to a timestamped slug).
	items := `[{"title":"///","content":"a"}]`
	dir := captureProject(t, items, "")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	res := callCapture(srv, "capture the untitled")
	if !res.IsError {
		t.Fatal("capture with an empty-after-sanitize slug must fail")
	}
	if got := captureFiles(t, dir); got != 0 {
		t.Errorf("rejected capture persisted %d files", got)
	}
}

func TestMCPCaptureInvalidUTF8Rejected(t *testing.T) {
	dir := captureProject(t, `[{"title":"X","content":"x"}]`, "")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	sink := &mcpCaptureSink{}
	srv.SetEventSink(sink)

	res := callCapture(srv, string([]byte{0xFF, 0xC3, 0x28, 'a'}))
	if !res.IsError {
		t.Fatal("invalid UTF-8 capture content must be rejected (text surface)")
	}
	if got := captureFiles(t, dir); got != 0 {
		t.Errorf("rejected capture persisted %d files", got)
	}
}

func TestMCPWriteArticleCharsetEnforced(t *testing.T) {
	dir := captureProject(t, "[]", "")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	for _, bad := range []string{"Bad_Concept", "../evil", "has space"} {
		res := srv.CallTool(context.Background(), "wiki_write_article", mcplib.CallToolRequest{
			Params: mcplib.CallToolParams{
				Name:      "wiki_write_article",
				Arguments: map[string]any{"concept": bad, "content": "body"},
			},
		})
		if !res.IsError {
			t.Errorf("wiki_write_article concept %q succeeded, want charset rejection", bad)
		}
	}

	res := srv.CallTool(context.Background(), "wiki_write_article", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_write_article",
			Arguments: map[string]any{"concept": "good-concept", "content": "body"},
		},
	})
	if res.IsError {
		t.Fatalf("benign concept rejected: %s", res.Content[0].(mcplib.TextContent).Text)
	}
	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "good-concept.md")); err != nil {
		t.Errorf("benign article missing: %v", err)
	}
}
