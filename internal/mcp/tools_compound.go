package mcp

import (
	"context"
	"fmt"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/ontology"
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
