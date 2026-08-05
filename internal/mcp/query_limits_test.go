package mcp

import (
	"context"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/xoai/sage-wiki/internal/wiki"
	"os"
	"path/filepath"
)

// SPEC-08 Task 10: max_query_bytes on the MCP query surfaces.

func queryLimitProject(t *testing.T) string {
	t.Helper()
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
	return dir
}

func TestMCPSearchQueryTooLarge(t *testing.T) {
	dir := queryLimitProject(t)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	res := srv.CallTool(context.Background(), "wiki_search", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_search",
			Arguments: map[string]any{"query": strings.Repeat("q", 32)},
		},
	})
	if !res.IsError {
		t.Fatal("overlong wiki_search query must be rejected")
	}
}

func TestMCPGraphQueryQuestionTooLarge(t *testing.T) {
	dir := queryLimitProject(t)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	res := srv.CallTool(context.Background(), "wiki_graph_query", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_graph_query",
			Arguments: map[string]any{"question": strings.Repeat("q", 32)},
		},
	})
	if !res.IsError {
		t.Fatal("overlong wiki_graph_query question must be rejected")
	}
}
