package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// Server wraps an MCP server with wiki tools.
type Server struct {
	mcp         *server.MCPServer
	projectDir  string
	db          store.DBHandle
	closeDB     func() error
	mem         *memory.Store
	vec         *vectors.Store
	ont         *ontology.Store
	searcher    *hybrid.Searcher
	cfg         *config.Config
	embedder    embed.Embedder
	language    string
	coordinator *compiler.CompileCoordinator // serializes compiles
}

// NewServer creates an MCP server with read tools registered, via the
// shared app container (P1-8). If coordinator is provided, it's used to
// serialize compile-on-demand with background compiles.
// All fields are aliased from it (s.db = a.DB, closeOnce-idempotent), and
// the embedder is built at exactly this point — the same construction
// moment as before (app.Embedder() is lazy; calling it here preserves the
// previous eager timing for the MCP server specifically).
func NewServer(projectDir string, coordinator ...*compiler.CompileCoordinator) (*Server, error) {
	a, err := app.Open(projectDir)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}

	var cc *compiler.CompileCoordinator
	if len(coordinator) > 0 && coordinator[0] != nil {
		cc = coordinator[0]
	} else {
		cc = compiler.NewCompileCoordinator()
	}

	s := &Server{
		projectDir:  projectDir,
		db:          a.DB,
		closeDB:     a.Close,
		mem:         a.Mem,
		vec:         a.Vec,
		ont:         a.Ont,
		searcher:    a.Searcher,
		cfg:         a.Config,
		embedder:    a.Embedder(),
		language:    a.Config.Language,
		coordinator: cc,
	}

	mcpServer := server.NewMCPServer(
		"sage-wiki",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	s.mcp = mcpServer
	s.registerReadTools()
	s.registerWriteTools()
	s.registerCompoundTools()

	return s, nil
}

// ServeStdio starts the MCP server on stdio transport.
func (s *Server) ServeStdio() error {
	defer s.closeDB()
	return server.ServeStdio(s.mcp)
}

// ServeSSE starts the MCP server on SSE transport (localhost only).
func (s *Server) ServeSSE(port int) error {
	defer s.closeDB()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	sseServer := server.NewSSEServer(s.mcp, server.WithBaseURL("http://"+addr))
	return sseServer.Start(addr)
}

// Close cleans up resources.
func (s *Server) Close() error {
	return s.closeDB()
}

// MCPServer returns the underlying MCP server for testing.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcp
}

// MemStore returns the memory store for testing.
func (s *Server) MemStore() *memory.Store { return s.mem }

// VecStore returns the vector store for testing.
func (s *Server) VecStore() *vectors.Store { return s.vec }

// OntStore returns the ontology store for testing.
func (s *Server) OntStore() *ontology.Store { return s.ont }

// CallTool invokes a tool handler by name. Used for testing.
func (s *Server) CallTool(ctx context.Context, name string, req mcp.CallToolRequest) *mcp.CallToolResult {
	handlers := map[string]func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){
		"wiki_search":        s.handleSearch,
		"wiki_read":          s.handleRead,
		"wiki_status":        s.handleStatus,
		"wiki_ontology_query": s.handleOntologyQuery,
		"wiki_list":          s.handleList,
		"wiki_add_source":    s.handleAddSource,
		"wiki_write_summary": s.handleWriteSummary,
		"wiki_write_article": s.handleWriteArticle,
		"wiki_add_ontology":  s.handleAddOntology,
		"wiki_learn":         s.handleLearn,
		"wiki_commit":        s.handleCommit,
		"wiki_compile_diff":  s.handleCompileDiff,
		"wiki_compile":       s.handleCompile,
		"wiki_lint":          s.handleLint,
		"wiki_capture":       s.handleCapture,
		"wiki_compile_topic": s.handleCompileTopic,
		"wiki_provenance":    s.handleProvenance,
	}
	if h, ok := handlers[name]; ok {
		r, _ := h(ctx, req)
		return r
	}
	return errorResult(fmt.Sprintf("unknown tool: %s", name))
}

func (s *Server) registerReadTools() {
	// wiki_search
	s.mcp.AddTool(
		mcp.NewTool("wiki_search",
			mcp.WithDescription("Search the wiki using hybrid BM25 + vector search. Returns ranked results."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithString("tags", mcp.Description("Comma-separated tag filter")),
			mcp.WithNumber("limit", mcp.Description("Maximum results (default 10)")),
		),
		s.handleSearch,
	)

	// wiki_read
	s.mcp.AddTool(
		mcp.NewTool("wiki_read",
			mcp.WithDescription("Read the full content of a wiki article by path."),
			mcp.WithString("path", mcp.Required(), mcp.Description("Article file path relative to project root")),
		),
		s.handleRead,
	)

	// wiki_status
	s.mcp.AddTool(
		mcp.NewTool("wiki_status",
			mcp.WithDescription("Show wiki stats: sources, concepts, entries, vectors, entities, relations."),
		),
		s.handleStatus,
	)

	// wiki_ontology_query
	s.mcp.AddTool(
		mcp.NewTool("wiki_ontology_query",
			mcp.WithDescription("Query the ontology graph. Traverse from an entity following typed relations."),
			mcp.WithString("entity", mcp.Required(), mcp.Description("Entity ID to start from")),
			mcp.WithString("relation", mcp.Description("Filter by relation type")),
			mcp.WithString("direction", mcp.Description("Traversal direction: outbound, inbound, both (default outbound)")),
			mcp.WithNumber("depth", mcp.Description("Traversal depth 1-5 (default 1)")),
		),
		s.handleOntologyQuery,
	)

	// wiki_list
	s.mcp.AddTool(
		mcp.NewTool("wiki_list",
			mcp.WithDescription("List wiki articles, optionally filtered by entity type."),
			mcp.WithString("type", mcp.Description("Filter by entity type: concept, technique, source, claim, artifact")),
		),
		s.handleList,
	)

	// wiki_provenance
	s.mcp.AddTool(
		mcp.NewTool("wiki_provenance",
			mcp.WithDescription("Show source-article provenance. Given a source path, returns generated articles. Given an article/concept name, returns contributing sources."),
			mcp.WithString("source", mcp.Description("Source file path (e.g. raw/paper.pdf)")),
			mcp.WithString("article", mcp.Description("Concept/article name (e.g. attention)")),
		),
		s.handleProvenance,
	)
}

func (s *Server) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	query, _ := args["query"].(string)
	if query == "" {
		return errorResult("query is required"), nil
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	var tags []string
	if t, ok := args["tags"].(string); ok && t != "" {
		for _, tag := range splitTags(t) {
			tags = append(tags, tag)
		}
	}

	var queryVec []float32
	if s.embedder != nil {
		var embedErr error
		queryVec, embedErr = s.embedder.Embed(query)
		if embedErr != nil {
			log.Warn("search embed failed, falling back to BM25-only", "error", embedErr)
		}
	}
	results, err := s.searcher.Search(hybrid.SearchOpts{
		Query:        query,
		Tags:         tags,
		Limit:        limit,
		BM25Weight:   s.cfg.Search.HybridWeightBM25,
		VectorWeight: s.cfg.Search.HybridWeightVector,
	}, queryVec)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Cap each result's content so a single search can't overflow the caller's
	// context (full text remains available via wiki_read).
	maxRunes := s.cfg.Search.ResultMaxCharsOrDefault()
	for i := range results {
		results[i].Content = truncateResultContent(results[i].Content, results[i].ArticlePath, maxRunes)
	}

	// Count uncompiled sources matching the query (search response signaling)
	uncompiledCount := s.countUncompiledMatches(query)

	// Record query hits for promotion tracking
	if uncompiledCount > 0 {
		items := compiler.NewCompileItemStore(s.db)
		tierMgr := compiler.NewTierManager(&s.cfg.Compiler, items)
		var hitPaths []string
		for _, r := range results {
			hitPaths = append(hitPaths, r.ID)
		}
		tierMgr.RecordQueryHit(hitPaths)
	}

	// Build response with optional signaling
	type searchResponse struct {
		Results           []hybrid.SearchResult `json:"results"`
		UncompiledSources int                   `json:"uncompiled_sources,omitempty"`
		CompileHint       string                `json:"compile_hint,omitempty"`
	}

	resp := searchResponse{Results: results}
	if uncompiledCount > 0 {
		resp.UncompiledSources = uncompiledCount
		resp.CompileHint = fmt.Sprintf(
			"Found %d matching sources that haven't been fully compiled. "+
				"Use wiki_compile_topic(\"%s\") to compile them for richer results.",
			uncompiledCount, query)
	}

	data, _ := json.MarshalIndent(resp, "", "  ")
	return textResult(string(data)), nil
}

// countUncompiledMatches counts FTS5 entries with "src:" prefix matching
// the query that are below Tier 3 in compile_items.
// (P2-1: moved behind the EntryStore seam — same query, same error→0
// tolerance, via the concrete sqlite store.)
func (s *Server) countUncompiledMatches(query string) int {
	count, _ := s.mem.CountUncompiled(query)
	return count
}

// truncateResultContent caps content to maxRunes, cutting on a rune boundary so
// multibyte text (CJK/Vietnamese) is never split into a U+FFFD replacement char.
// When it truncates, it appends a marker pointing at wiki_read for the full text.
func truncateResultContent(content, articlePath string, maxRunes int) string {
	if maxRunes <= 0 {
		return content
	}
	// Find the byte offset of the (maxRunes)-th rune. range yields rune starts,
	// so cutting at this index keeps exactly maxRunes whole runes.
	count := 0
	cut := -1
	for i := range content {
		if count == maxRunes {
			cut = i
			break
		}
		count++
	}
	if cut < 0 {
		return content // content has <= maxRunes runes; nothing to trim
	}
	marker := fmt.Sprintf("\n\n[… truncated to %d chars", maxRunes)
	if articlePath != "" {
		marker += fmt.Sprintf("; call wiki_read(%q) for the full text", articlePath)
	}
	marker += "]"
	return content[:cut] + marker
}

func (s *Server) handleRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	path, _ := args["path"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}

	fullPath := filepath.Join(s.projectDir, path)

	// Prevent path traversal — resolved path must stay within project
	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(fullPath)
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to read %s: %v", path, err)), nil
	}

	return textResult(string(content)), nil
}

func (s *Server) handleStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	info, err := wiki.GetStatus(s.projectDir, &wiki.Stores{
		Mem: s.mem,
		Vec: s.vec,
		Ont: s.ont,
		DB:  s.db,
	})
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return textResult(wiki.FormatStatus(info)), nil
}

func (s *Server) handleOntologyQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	entityID, _ := args["entity"].(string)
	if entityID == "" {
		return errorResult("entity is required"), nil
	}

	dir := ontology.Outbound
	if d, ok := args["direction"].(string); ok {
		switch d {
		case "inbound":
			dir = ontology.Inbound
		case "both":
			dir = ontology.Both
		}
	}

	depth := 1
	if d, ok := args["depth"].(float64); ok {
		depth = int(d)
	}

	relType, _ := args["relation"].(string)

	entities, err := s.ont.Traverse(entityID, ontology.TraverseOpts{
		Direction:    dir,
		RelationType: relType,
		MaxDepth:     depth,
	})
	if err != nil {
		return errorResult(err.Error()), nil
	}

	data, _ := json.MarshalIndent(entities, "", "  ")
	return textResult(string(data)), nil
}

func (s *Server) handleList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	entityType, _ := args["type"].(string)

	entities, err := s.ont.ListEntities(entityType)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Also load manifest for source/concept counts
	mfPath := filepath.Join(s.projectDir, ".manifest.json")
	mf, _ := manifest.Load(mfPath)

	type listItem struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		ArticlePath string `json:"article_path,omitempty"`
	}

	items := make([]listItem, len(entities))
	for i, e := range entities {
		items[i] = listItem{ID: e.ID, Type: e.Type, Name: e.Name, ArticlePath: e.ArticlePath}
	}

	result := map[string]any{
		"entities":      items,
		"total":         len(items),
		"source_count":  mf.SourceCount(),
		"concept_count": mf.ConceptCount(),
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(data)), nil
}

func (s *Server) handleProvenance(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	source, _ := args["source"].(string)
	article, _ := args["article"].(string)

	if source == "" && article == "" {
		return errorResult("either 'source' or 'article' parameter is required"), nil
	}

	mfPath := filepath.Join(s.projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return errorResult(fmt.Sprintf("load manifest: %v", err)), nil
	}

	var result map[string]any

	if source != "" {
		articles := mf.ArticlesFromSource(source)
		items := make([]map[string]string, 0, len(articles))
		for _, name := range articles {
			c := mf.Concepts[name]
			items = append(items, map[string]string{"concept": name, "article_path": c.ArticlePath})
		}
		result = map[string]any{"source": source, "articles": items, "total": len(items)}
	} else {
		sources := mf.SourcesForArticle(article)
		result = map[string]any{"article": article, "sources": sources, "total": len(sources)}
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(data)), nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: text},
		},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
	}
}

func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// isSubpath checks that child is inside parent directory. It delegates to the
// shared pathsafe helper so every MCP call site gets symlink-safe, fail-closed
// containment (a symlink inside parent that points out cannot escape). On any
// resolution error it returns false — deny by default.
func isSubpath(parent, child string) bool {
	ok, err := pathsafe.Contained(parent, child)
	return err == nil && ok
}
