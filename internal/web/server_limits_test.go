package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// SPEC-08 Task 10: /api/query rejects overlong questions with 413 before
// any provider call.
func TestHandleQueryTooLarge(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "querylimit", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_query_bytes: 16\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	defer srv.Close()

	body := `{"question":"` + strings.Repeat("q", 32) + `"}`
	req := httptest.NewRequest("POST", "/api/query", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != 413 {
		t.Fatalf("status = %d, want 413 (body: %s)", w.Code, w.Body.String())
	}
}
