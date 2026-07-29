package sagewiki_test

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/sagewiki"
)

// setupProject initializes a greenfield project the same way the CLI's
// `sage-wiki init` does, since NewServer expects an existing project.
func setupProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test-project", "gemini-2.5-flash"); err != nil {
		t.Fatalf("InitGreenfield: %v", err)
	}
	return dir
}

// TestInProcessEmbedding is the end-to-end case this package exists for:
// construct a server, hand it to mcp-go's in-process transport, complete the
// MCP handshake, and list the wiki tools — no subprocess, no stdio, no port.
func TestInProcessEmbedding(t *testing.T) {
	srv, err := sagewiki.NewServer(setupProject(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	cli, err := client.NewInProcessClient(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	initResult, err := cli.Initialize(ctx, mcp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := initResult.ServerInfo.Name; got != "sage-wiki" {
		t.Errorf("ServerInfo.Name = %q, want %q", got, "sage-wiki")
	}
	// Version is ldflags-injected; from a plain `go test` it is the default.
	if initResult.ServerInfo.Version == "" {
		t.Error("ServerInfo.Version is empty")
	}

	tools, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	// Spot-check one read tool from each registration group rather than
	// pinning the full set, which grows release to release.
	want := []string{"wiki_search", "wiki_status", "wiki_compile"}
	got := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		got[tool.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// TestCallToolInProcess confirms a tool actually executes against the
// project's database through the transport, not just that it is listed.
func TestCallToolInProcess(t *testing.T) {
	srv, err := sagewiki.NewServer(setupProject(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	cli, err := client.NewInProcessClient(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	result, err := cli.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "wiki_status"},
	})
	if err != nil {
		t.Fatalf("CallTool(wiki_status): %v", err)
	}
	if result.IsError {
		t.Fatalf("wiki_status returned an error result: %+v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Error("wiki_status returned no content")
	}
}

// TestNewServerRejectsUninitializedDir documents that NewServer opens an
// existing project rather than creating one.
func TestNewServerRejectsUninitializedDir(t *testing.T) {
	srv, err := sagewiki.NewServer(t.TempDir())
	if err == nil {
		srv.Close()
		t.Fatal("expected an error for a directory with no config.yaml")
	}
}

// TestCloseIsIdempotent covers the documented promise that callers can Close
// more than once (App.Close wraps a closeOnce handle).
func TestCloseIsIdempotent(t *testing.T) {
	srv, err := sagewiki.NewServer(setupProject(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestSetVersionIsReportedOnInitialize covers the documented escape hatch for
// embedders that want their own version in the initialize response instead of
// the sage-wiki build version.
func TestSetVersionIsReportedOnInitialize(t *testing.T) {
	const want = "host-app/9.9.9"
	sagewiki.SetVersion(want)
	t.Cleanup(func() { sagewiki.SetVersion("dev") })

	srv, err := sagewiki.NewServer(setupProject(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	cli, err := client.NewInProcessClient(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	if err := cli.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	initResult, err := cli.Initialize(ctx, mcp.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := initResult.ServerInfo.Version; got != want {
		t.Errorf("ServerInfo.Version = %q, want %q", got, want)
	}
}
