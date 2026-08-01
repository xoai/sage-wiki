package parity

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/serve"
)

// TestMCPStreamableHTTP is AC-S3: an MCP client over streamable HTTP
// lists all 19 tools and runs wiki_graph_query against the golden corpus.
func TestMCPStreamableHTTP(t *testing.T) {
	if suiteWS == "" {
		t.Skip("shared workspace not built (SAGE_PARITY_FORCE=1 mode)")
	}
	deps, err := serve.AssembleDeps(suiteWS)
	if err != nil {
		t.Fatal(err)
	}
	defer deps.Close()
	mcpSrv, err := mcppkg.NewServer(suiteWS, deps.Coordinator())
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()

	srv, err := serve.New(deps, mcpSrv, serve.Config{
		Workspace: suiteWS,
		ReadyFn:   func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx := context.Background()
	c, err := client.NewStreamableHttpClient(httpSrv.URL + "/mcp")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.Close()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "parity-test", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 19 {
		t.Errorf("tools = %d, want 19", len(tools.Tools))
	}

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "wiki_graph_query",
			Arguments: map[string]any{"question": "api gateway", "mode": "local"},
		},
	})
	if err != nil {
		t.Fatalf("wiki_graph_query: %v", err)
	}
	if res.IsError {
		t.Fatalf("wiki_graph_query error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Error("wiki_graph_query returned no content")
	}
}
