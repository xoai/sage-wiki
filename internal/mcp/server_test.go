package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize a greenfield project
	if err := wiki.InitGreenfield(dir, "test-project", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	return dir
}

func TestNewServer(t *testing.T) {
	dir := setupTestProject(t)

	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	if srv.MCPServer() == nil {
		t.Error("expected non-nil MCP server")
	}
}

func TestHandleSearch(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	// Add some test data
	srv.mem.Add(memory.Entry{
		ID:          "e1",
		Content:     "attention mechanism in transformers",
		Tags:        []string{"attention"},
		ArticlePath: "wiki/concepts/attention.md",
	})

	result, err := srv.handleSearch(context.Background(), makeToolRequest(map[string]any{
		"query": "attention",
		"limit": float64(5),
	}))
	if err != nil {
		t.Fatalf("handleSearch: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content[0].(mcp.TextContent).Text)
	}

	// Parse results (wrapped in searchResponse object)
	text := result.Content[0].(mcp.TextContent).Text
	var resp struct {
		Results           []map[string]any `json:"results"`
		UncompiledSources int              `json:"uncompiled_sources"`
		CompileHint       string           `json:"compile_hint"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse results: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected search results")
	}
}

func TestHandleRead(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	// Write a test article
	articlePath := filepath.Join(dir, "wiki", "concepts", "test.md")
	os.WriteFile(articlePath, []byte("# Test Article\nContent here."), 0644)

	result, err := srv.handleRead(context.Background(), makeToolRequest(map[string]any{
		"path": "wiki/concepts/test.md",
	}))
	if err != nil {
		t.Fatalf("handleRead: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content[0].(mcp.TextContent).Text)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text != "# Test Article\nContent here." {
		t.Errorf("unexpected content: %q", text)
	}
}

func TestHandleReadPathTraversal(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	// Attempt path traversal
	result, _ := srv.handleRead(context.Background(), makeToolRequest(map[string]any{
		"path": "../../etc/passwd",
	}))
	if !result.IsError {
		t.Error("expected error for path traversal")
	}
	text := result.Content[0].(mcp.TextContent).Text
	if text != "path traversal not allowed" {
		t.Errorf("expected traversal error, got: %s", text)
	}
}

func TestHandleReadMissing(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	result, _ := srv.handleRead(context.Background(), makeToolRequest(map[string]any{
		"path": "nonexistent.md",
	}))
	if !result.IsError {
		t.Error("expected error for missing file")
	}
}

func TestHandleStatus(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	result, err := srv.handleStatus(context.Background(), makeToolRequest(nil))
	if err != nil {
		t.Fatalf("handleStatus: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content[0].(mcp.TextContent).Text)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if text == "" {
		t.Error("expected non-empty status")
	}
}

func TestHandleOntologyQuery(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	// Add entities and relations
	srv.ont.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention"})
	srv.ont.AddEntity(ontology.Entity{ID: "flash-attn", Type: "technique", Name: "Flash Attention"})
	srv.ont.AddRelation(ontology.Relation{ID: "r1", SourceID: "flash-attn", TargetID: "attention", Relation: "implements"})

	result, err := srv.handleOntologyQuery(context.Background(), makeToolRequest(map[string]any{
		"entity":    "flash-attn",
		"direction": "outbound",
		"depth":     float64(1),
	}))
	if err != nil {
		t.Fatalf("handleOntologyQuery: %v", err)
	}
	if result.IsError {
		t.Errorf("error: %s", result.Content[0].(mcp.TextContent).Text)
	}

	var entities []map[string]any
	json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &entities)
	if len(entities) != 1 {
		t.Errorf("expected 1 entity, got %d", len(entities))
	}
}

func TestHandleList(t *testing.T) {
	dir := setupTestProject(t)
	srv, _ := NewServer(dir)
	defer srv.Close()

	srv.ont.AddEntity(ontology.Entity{ID: "e1", Type: "concept", Name: "A"})
	srv.ont.AddEntity(ontology.Entity{ID: "e2", Type: "technique", Name: "B"})

	// List only concepts
	result, _ := srv.handleList(context.Background(), makeToolRequest(map[string]any{
		"type": "concept",
	}))

	text := result.Content[0].(mcp.TextContent).Text
	var listResult map[string]any
	json.Unmarshal([]byte(text), &listResult)

	entities := listResult["entities"].([]any)
	if len(entities) != 1 {
		t.Errorf("expected 1 concept, got %d", len(entities))
	}
}

// makeToolRequest creates a CallToolRequest with the given arguments.
func makeToolRequest(args map[string]any) mcp.CallToolRequest {
	if args == nil {
		args = map[string]any{}
	}
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "test",
			Arguments: args,
		},
	}
}

// TestNewServer_ServesAppData: the adopted container shares state — an
// entry written via a separate app.Open is visible to the MCP server's
// stores (construction parity, P1-8 T3).
func TestNewServer_ServesAppData(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	a, err := app.Open(dir)
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	if err := a.Mem.Add(memory.Entry{ID: "concept:parity", Content: "container parity content"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	a.Close()

	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	n, err := srv.mem.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("server mem count = %d, want 1 (written via the container)", n)
	}
}
