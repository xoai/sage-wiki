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
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// Server wraps an MCP server with wiki tools.
type Server struct {
	mcp        *server.MCPServer
	projectDir string
	db         *storage.DB
	mem        *memory.Store
	vec        *vectors.Store
	ont        *ontology.Store
	searcher   *hybrid.Searcher
	embedder   embed.Embedder
	cfg        *config.Config
	language   string
}

// NewServer creates an MCP server with read tools registered.
func NewServer(projectDir string) (*Server, error) {
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("mcp: load config: %w", err)
	}

	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("mcp: open db: %w", err)
	}

	mem := memory.NewStore(db)
	vec := vectors.NewStore(db)
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ont := ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes))
	searcher := hybrid.NewSearcher(mem, vec)

	s := &Server{
		projectDir: projectDir,
		db:         db,
		mem:        mem,
		vec:        vec,
		ont:        ont,
		searcher:   searcher,
		embedder:   embed.NewFromConfig(cfg),
		cfg:        cfg,
		language:   cfg.Language,
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
	defer s.db.Close()
	return server.ServeStdio(s.mcp)
}

// ServeSSE starts the MCP server on SSE transport (localhost only).
func (s *Server) ServeSSE(port int) error {
	defer s.db.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	sseServer := server.NewSSEServer(s.mcp, server.WithBaseURL("http://"+addr))
	return sseServer.Start(addr)
}

// Close cleans up resources.
func (s *Server) Close() error {
	return s.db.Close()
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
			fmt.Fprintf(os.Stderr, "warn: search embed failed, falling back to BM25-only: %v\n", embedErr)
		}
	}
	results, err := s.searcher.Search(hybrid.SearchOpts{
		Query: query,
		Tags:  tags,
		Limit: limit,
	}, queryVec)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	data, _ := json.MarshalIndent(results, "", "  ")
	return textResult(string(data)), nil
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

// isSubpath checks that child is inside parent directory.
func isSubpath(parent, child string) bool {
	parent = filepath.Clean(parent) + string(filepath.Separator)
	child = filepath.Clean(child)
	return strings.HasPrefix(child, parent) || child == filepath.Clean(parent[:len(parent)-1])
}
