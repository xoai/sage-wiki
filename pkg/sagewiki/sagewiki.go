// Package sagewiki is the supported entry point for embedding sage-wiki in
// another Go program, in-process, without spawning `sage-wiki serve` as a
// subprocess (#112).
//
// It deliberately exposes one thing: the MCP server handle. Everything a
// caller can do, it does by calling the same MCP tools an editor or agent
// integration calls over stdio — the compiler, store, and config internals
// stay unexported. Pair it with mcp-go's in-process transport:
//
//	srv, err := sagewiki.NewServer("/path/to/wiki-project")
//	if err != nil {
//		return err
//	}
//	defer srv.Close()
//
//	cli, err := client.NewInProcessClient(srv.MCPServer())
//	if err != nil {
//		return err
//	}
//	defer cli.Close()
//
//	if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
//		return err
//	}
//	res, err := cli.CallTool(ctx, mcp.CallToolRequest{
//		Params: mcp.CallToolParams{
//			Name:      "wiki_search",
//			Arguments: map[string]any{"query": "attention", "limit": 5},
//		},
//	})
//
// The project directory must already be initialized (`sage-wiki init`, or
// the equivalent init tooling): NewServer opens an existing config.yaml and
// .sage/wiki.db, it does not create them.
//
// # Experimental
//
// sage-wiki is pre-1.0 and this package is experimental. The Go signatures
// here are small and meant to stay put, but what they give you access to —
// MCP tool names, their argument schemas, config.yaml layout, and tool
// behavior — can change in any release. Pin a version.
//
// # Logging
//
// sage-wiki logs to the host process's stderr (see internal/log). An
// embedder cannot currently redirect that.
//
// # Version reporting
//
// The initialize response carries the sage-wiki build version, which is `dev`
// unless the binary was built with the release ldflags — an embedded server
// gets `dev` by default. Call SetVersion during startup to report your own
// version string instead.
package sagewiki

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/xoai/sage-wiki/internal/mcp"
)

// Server is a live sage-wiki MCP server bound to one project directory.
type Server struct {
	inner *mcp.Server
}

// NewServer opens the project at projectDir and returns a server with the
// wiki tools registered — the same set `sage-wiki serve` exposes.
//
// The caller owns the returned Server and must Close it: unlike the serve
// commands, nothing else closes the underlying database.
func NewServer(projectDir string) (*Server, error) {
	inner, err := mcp.NewServer(projectDir)
	if err != nil {
		return nil, err
	}
	return &Server{inner: inner}, nil
}

// MCPServer returns the underlying MCP server, for wiring into a transport
// such as mcp-go's client.NewInProcessClient.
//
// The Server retains ownership: the handle stops working once Close runs.
func (s *Server) MCPServer() *server.MCPServer {
	return s.inner.MCPServer()
}

// Close releases the project's database handle. It is safe to call more
// than once.
func (s *Server) Close() error {
	return s.inner.Close()
}

// SetVersion overrides the version string this server reports to clients in
// the initialize response. Without it, clients see the sage-wiki build
// version (`dev` from a plain `go build`) — an embedder that would rather
// report its own version calls this first.
//
// It applies process-wide to servers created afterwards, so call it during
// startup, before NewServer, and not concurrently with it.
func SetVersion(version string) {
	mcp.Version = version
}
