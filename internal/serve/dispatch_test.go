package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/api"
)

// TestTranslateToolResultRedaction pins R-01: path-sensitive tools get
// the generic message; others keep the text.
func TestTranslateToolResultRedaction(t *testing.T) {
	errRes := &mcpgo.CallToolResult{
		IsError: true,
		Content: []mcpgo.Content{mcpgo.TextContent{Type: "text", Text: "open /home/user/secret/wiki/x.md: no such file"}},
	}
	isErr, body := api.TranslateToolResult("wiki_read", errRes)
	if !isErr {
		t.Fatal("expected error translation")
	}
	var envelope map[string]string
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(envelope["error"], "/home/user") {
		t.Errorf("path leaked in redacted error: %s", envelope["error"])
	}
	if envelope["error"] != "wiki_read failed" {
		t.Errorf("redacted message = %q", envelope["error"])
	}

	isErr, body = api.TranslateToolResult("wiki_search", errRes)
	if !isErr {
		t.Fatal("expected error translation")
	}
	json.Unmarshal(body, &envelope)
	if !strings.Contains(envelope["error"], "/home/user") {
		t.Errorf("non-sensitive tool must keep error text: %s", envelope["error"])
	}
}

// TestDocNotFoundNoPathInBody pins N-03: an unknown article returns 404
// with no path classification from error text.
func TestDocNotFoundNoPathInBody(t *testing.T) {
	srv := testServer(t, nil)
	h := srv.Handler()
	r := httptest.NewRequest("GET", "/docs/wiki/concepts/no-such-article.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown article = %d, want 404", w.Code)
	}
}
