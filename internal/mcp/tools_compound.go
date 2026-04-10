package mcp

import (
	"context"
	"fmt"
	"path/filepath"

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

	result, err := compiler.Compile(s.projectDir, compiler.CompileOpts{
		DryRun: dryRun,
		Fresh:  fresh,
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

	merged := ontology.MergedRelations(s.cfg.Ontology.Relations)
	lintCtx := &linter.LintContext{
		ProjectDir:     s.projectDir,
		OutputDir:      s.cfg.Output,
		DBPath:         filepath.Join(s.projectDir, ".sage", "wiki.db"),
		DB:             s.db,
		ValidRelations: ontology.ValidRelationNames(merged),
	}

	runner := linter.NewRunner()
	results, err := runner.Run(lintCtx, passName, fix)
	if err != nil {
		return errorResult(fmt.Sprintf("lint failed: %v", err)), nil
	}

	return textResult(linter.FormatFindings(results)), nil
}
