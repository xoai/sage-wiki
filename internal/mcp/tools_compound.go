package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/query"
)

func (s *Server) registerCompoundTools() {
	s.mcp.AddTool(
		mcplib.NewTool("wiki_compile",
			mcplib.WithDescription("Run the full compile pipeline: diff → summarize → extract concepts → write articles."),
			mcplib.WithBoolean("dry_run", mcplib.Description("Show what would change without writing")),
			mcplib.WithBoolean("fresh", mcplib.Description("Ignore checkpoint, clean compile")),
			mcplib.WithBoolean("prune", mcplib.Description("Remove orphaned articles whose sole source was deleted")),
		),
		s.handleCompile,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_lint",
			mcplib.WithDescription("Run linting passes on the wiki. Returns findings with severity and suggestions."),
			mcplib.WithString("pass", mcplib.Description("Specific lint pass to run")),
			mcplib.WithBoolean("fix", mcplib.Description("Auto-fix issues")),
		),
		s.handleLint,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_query",
			mcplib.WithDescription("Ask a free-form question against the wiki: searches sources and compiled articles, synthesizes a cited answer with the LLM (spends LLM budget), and files the result to wiki/under_review/ by default (trust output review) or wiki/outputs/ only when trust include_outputs is 'true'. Returns the answer, source paths, and the filed path."),
			mcplib.WithString("question", mcplib.Required(), mcplib.Description("Natural-language question")),
			mcplib.WithNumber("top_k", mcplib.Description("Sources to synthesize from, 1-20 (default 5)")),
		),
		s.handleQuery,
	)
}

// handleQuery answers a free-form question via query.Query — the exact CLI
// `query` pipeline (search → LLM synthesis → auto-file per trust mode),
// sharing the server's DB handle. Issue #125.
func (s *Server) handleQuery(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	question, _ := args["question"].(string)
	if strings.TrimSpace(question) == "" {
		return errorResult("question is required"), nil
	}
	if err := s.checkQueryLen(question); err != nil {
		return errorResult(fmt.Sprintf("question rejected: %v", err)), nil
	}
	topK := 5
	if k, ok := args["top_k"].(float64); ok && k != 0 {
		// k != k catches NaN on the in-process CallTool path (JSON can't
		// carry NaN, but embedders via pkg/sagewiki can pass it).
		if k != k || k < 1 || k > 20 {
			return errorResult("top_k must be between 1 and 20"), nil
		}
		topK = int(k) // in-range fractionals truncate (5.7 → 5); out-of-range already errored
	}

	result, err := query.Query(s.projectDir, question, "markdown", topK, query.QueryOpts{DB: s.db})
	if err != nil {
		// query.Query already prefixes its errors ("query: create LLM
		// client: …") — pass through verbatim to avoid a doubled prefix.
		return errorResult(err.Error()), nil
	}
	// query.Query chunk-indexes the filed output using a FRESH vectors.Store
	// and invalidates that instance's cache; the server's long-lived store
	// must be invalidated too or subsequent searches serve stale chunks.
	if result.OutputPath != "" {
		s.vec.InvalidateChunkCache()
	}

	sources := result.Sources
	if sources == nil {
		sources = []string{} // strict-typed clients reject "sources": null
	}
	resp := map[string]any{
		"answer":      result.Answer,
		"sources":     sources,
		"output_path": result.OutputPath,
	}
	// Filing failures must surface: success + output_path "" would be
	// indistinguishable from the benign no-content short-circuit.
	if result.FilingError != "" {
		resp["filing_error"] = result.FilingError
	}
	data, _ := json.MarshalIndent(resp, "", "  ")
	return textResult(string(data)), nil
}

func (s *Server) handleCompile(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	dryRun, _ := args["dry_run"].(bool)
	fresh, _ := args["fresh"].(bool)
	prune, _ := args["prune"].(bool)

	result, err := compiler.Compile(s.projectDir, compiler.CompileOpts{
		Ctx:     ctx,
		DryRun:  dryRun,
		Fresh:   fresh,
		Prune:   prune,
		Backend: s.backend,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("compile failed: %v", err)), nil
	}

	summary := fmt.Sprintf("Compile complete:\n- Added: %d\n- Modified: %d\n- Removed: %d\n- Summarized: %d\n- Concepts: %d\n- Articles: %d\n- Errors: %d",
		result.Added, result.Modified, result.Removed,
		result.Summarized, result.ConceptsExtracted, result.ArticlesWritten, result.Errors)

	return textResult(summary), nil
}

func (s *Server) handleLint(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	passName, _ := args["pass"].(string)
	fix, _ := args["fix"].(bool)

	results, err := s.RunLint(passName, fix)
	if err != nil {
		return errorResult(fmt.Sprintf("lint failed: %v", err)), nil
	}

	return textResult(linter.FormatFindings(results)), nil
}

// RunLint executes the linter over the server's project. Exported so the
// REST job runner (P4-2) shares the exact MCP wiring.
func (s *Server) RunLint(passName string, fix bool) ([]linter.LintResult, error) {
	mergedRels := ontology.MergedRelations(s.cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(s.cfg.Ontology.EntityTypes)
	lintCtx := &linter.LintContext{
		ProjectDir: s.projectDir,
		OutputDir:  s.cfg.Output,
		// DBPath omitted: DB is always set here, so EnsureDB never opens a
		// fallback (P2-1: a bare path has no meaning for the postgres backend).
		DB:               s.db,
		ValidRelations:   ontology.ValidRelationNames(mergedRels),
		ValidEntityTypes: ontology.ValidEntityTypeNames(mergedTypes),
		QualityThreshold: s.cfg.Compiler.QualityThreshold(),
		TemporalEnabled:  s.cfg.Ontology.Temporal.Enabled,
	}

	runner := linter.NewRunner()
	return runner.Run(lintCtx, passName, fix)
}
