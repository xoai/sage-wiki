package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/wiki"
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

// TestHandleDocRejectsEncodedTraversal: an encoded `..` (which bypasses
// ServeMux's cleanPath 307) must not reach os.Stat on a workspace-escaped
// path — that Stat is a host-filesystem existence oracle. The workspace is
// a subdir of a parent that ALSO holds a marker file, so the escaped path
// EXISTS; without the containment gate the handler would proceed past Stat
// (the oracle: existing-vs-missing return different status). Containment
// before the lookup makes traversal always return the generic 404.
func TestHandleDocRejectsEncodedTraversal(t *testing.T) {
	parent := t.TempDir()
	wsDir := filepath.Join(parent, "ws")
	if err := wiki.InitGreenfield(wsDir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	// A file OUTSIDE the workspace, inside its parent — the escape target.
	if err := os.WriteFile(filepath.Join(parent, "secret-marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(wsDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deps.Close)
	srv, err := New(deps, nil, Config{Workspace: wsDir, ReadyFn: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	r := httptest.NewRequest("GET", "/docs/%2e%2e/secret-marker", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("traversal to an EXISTING escaped file = %d, want 404 (no existence oracle)", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret-marker") {
		t.Errorf("response echoed the probed path: %s", w.Body.String())
	}
}
