package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/embed"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
)

func (s *Server) registerWriteTools() {
	s.mcp.AddTool(
		mcplib.NewTool("wiki_add_source",
			mcplib.WithDescription("Add a source file to a source folder and update the manifest."),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path relative to project root")),
			mcplib.WithString("type", mcplib.Description("Source type: article, paper, code (default: auto-detect)")),
		),
		s.handleAddSource,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_write_summary",
			mcplib.WithDescription("Write a summary markdown file, index in FTS5, and optionally embed vector."),
			mcplib.WithString("source", mcplib.Required(), mcplib.Description("Source file path this summary is for")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Summary markdown content")),
			mcplib.WithString("concepts", mcplib.Description("Comma-separated concept names extracted")),
		),
		s.handleWriteSummary,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_write_article",
			mcplib.WithDescription("Write a concept article, create ontology entity, and embed vector."),
			mcplib.WithString("concept", mcplib.Required(), mcplib.Description("Concept ID (lowercase-hyphenated)")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Article markdown content with frontmatter")),
		),
		s.handleWriteArticle,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_add_ontology",
			mcplib.WithDescription("Create an ontology entity or relation."),
			mcplib.WithString("entity_id", mcplib.Description("Entity ID to create")),
			mcplib.WithString("entity_type", mcplib.Description("Entity type: concept, technique, source, claim, artifact")),
			mcplib.WithString("entity_name", mcplib.Description("Human-readable entity name")),
			mcplib.WithString("source_id", mcplib.Description("Relation source entity ID")),
			mcplib.WithString("target_id", mcplib.Description("Relation target entity ID")),
			mcplib.WithString("relation", mcplib.Description("Relation type: implements, extends, optimizes, etc.")),
		),
		s.handleAddOntology,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_learn",
			mcplib.WithDescription("Store a learning entry for the self-learning loop."),
			mcplib.WithString("type", mcplib.Required(), mcplib.Description("Learning type: gotcha, correction, convention, error-fix, api-drift")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("What was learned")),
			mcplib.WithString("tags", mcplib.Description("Comma-separated tags")),
		),
		s.handleLearn,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_commit",
			mcplib.WithDescription("Git add and commit all changes."),
			mcplib.WithString("message", mcplib.Description("Commit message (auto-generated if omitted)")),
		),
		s.handleCommit,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_compile_diff",
			mcplib.WithDescription("Show added/modified/removed source files compared to the manifest."),
		),
		s.handleCompileDiff,
	)
}

func (s *Server) handleAddSource(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	path, _ := args["path"].(string)
	if path == "" {
		return errorResult("path is required"), nil
	}

	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(filepath.Join(s.projectDir, path))
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return errorResult(fmt.Sprintf("file not found: %s", path)), nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to read: %v", err)), nil
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(data))

	srcType, _ := args["type"].(string)
	if srcType == "" {
		srcType = "article"
	}

	mf, err := manifest.Load(filepath.Join(s.projectDir, ".manifest.json"))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	mf.AddSource(path, hash, srcType, info.Size())
	if err := mf.Save(filepath.Join(s.projectDir, ".manifest.json")); err != nil {
		return errorResult(err.Error()), nil
	}

	return textResult(fmt.Sprintf("Source added: %s (type: %s, %d bytes)", path, srcType, info.Size())), nil
}

func (s *Server) handleWriteSummary(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	source, _ := args["source"].(string)
	content, _ := args["content"].(string)
	if source == "" || content == "" {
		return errorResult("source and content are required"), nil
	}

	baseName := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	summaryPath := filepath.Join(s.cfg.Output, "summaries", baseName+".md")
	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(filepath.Join(s.projectDir, summaryPath))
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}
	os.MkdirAll(filepath.Dir(absPath), 0755)

	frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\n---\n\n", source, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(absPath, []byte(frontmatter+content), 0644); err != nil {
		return errorResult(fmt.Sprintf("write failed: %v", err)), nil
	}

	s.mem.Add(memory.Entry{ID: source, Content: content, ArticlePath: summaryPath})
	s.tryEmbed(source, content)

	conceptsStr, _ := args["concepts"].(string)
	var concepts []string
	if conceptsStr != "" {
		for _, c := range strings.Split(conceptsStr, ",") {
			if c = strings.TrimSpace(c); c != "" {
				concepts = append(concepts, c)
			}
		}
	}

	mf, err := manifest.Load(filepath.Join(s.projectDir, ".manifest.json"))
	if err != nil {
		return errorResult(fmt.Sprintf("manifest load failed: %v", err)), nil
	}
	if _, exists := mf.Sources[source]; !exists {
		mf.AddSource(source, "", "article", int64(len(content)))
	}
	mf.MarkCompiled(source, summaryPath, concepts)
	if err := mf.Save(filepath.Join(s.projectDir, ".manifest.json")); err != nil {
		return errorResult(fmt.Sprintf("manifest save failed: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Summary written: %s (%d chars)", summaryPath, len(content))), nil
}

func (s *Server) handleWriteArticle(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	conceptID, _ := args["concept"].(string)
	content, _ := args["content"].(string)
	if conceptID == "" || content == "" {
		return errorResult("concept and content are required"), nil
	}

	articlePath := filepath.Join(s.cfg.Output, "concepts", conceptID+".md")
	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(filepath.Join(s.projectDir, articlePath))
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}
	os.MkdirAll(filepath.Dir(absPath), 0755)

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return errorResult(fmt.Sprintf("write failed: %v", err)), nil
	}

	s.ont.AddEntity(ontology.Entity{
		ID: conceptID, Type: ontology.TypeConcept, Name: conceptID, ArticlePath: articlePath,
	})
	s.mem.Add(memory.Entry{ID: "concept:" + conceptID, Content: content, ArticlePath: articlePath})
	s.tryEmbed("concept:"+conceptID, content)

	mf, err := manifest.Load(filepath.Join(s.projectDir, ".manifest.json"))
	if err != nil {
		return errorResult(fmt.Sprintf("manifest load failed: %v", err)), nil
	}
	mf.AddConcept(conceptID, articlePath, nil)
	if err := mf.Save(filepath.Join(s.projectDir, ".manifest.json")); err != nil {
		return errorResult(fmt.Sprintf("manifest save failed: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Article written: %s", articlePath)), nil
}

func (s *Server) handleAddOntology(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()

	entityID, _ := args["entity_id"].(string)
	sourceID, _ := args["source_id"].(string)

	if entityID != "" {
		entityType, _ := args["entity_type"].(string)
		entityName, _ := args["entity_name"].(string)
		if entityType == "" {
			entityType = "concept"
		}
		if entityName == "" {
			entityName = entityID
		}
		if err := s.ont.AddEntity(ontology.Entity{ID: entityID, Type: entityType, Name: entityName}); err != nil {
			return errorResult(fmt.Sprintf("add entity failed: %v", err)), nil
		}
		return textResult(fmt.Sprintf("Entity created: %s (%s)", entityID, entityType)), nil
	}

	if sourceID != "" {
		targetID, _ := args["target_id"].(string)
		relType, _ := args["relation"].(string)
		if targetID == "" || relType == "" {
			return errorResult("target_id and relation required for relations"), nil
		}
		if err := s.ont.AddRelation(ontology.Relation{
			ID: sourceID + "-" + relType + "-" + targetID, SourceID: sourceID, TargetID: targetID, Relation: relType,
		}); err != nil {
			return errorResult(fmt.Sprintf("add relation failed: %v", err)), nil
		}
		return textResult(fmt.Sprintf("Relation: %s -[%s]-> %s", sourceID, relType, targetID)), nil
	}

	return errorResult("provide entity_id or source_id+target_id+relation"), nil
}

func (s *Server) handleLearn(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	learnType, _ := args["type"].(string)
	content, _ := args["content"].(string)
	if learnType == "" || content == "" {
		return errorResult("type and content are required"), nil
	}
	tagsStr, _ := args["tags"].(string)

	if err := linter.StoreLearning(s.db, learnType, content, tagsStr, "mcp"); err != nil {
		return errorResult(fmt.Sprintf("store failed: %v", err)), nil
	}
	return textResult(fmt.Sprintf("Learning stored: [%s] %s", learnType, truncate(content, 80))), nil
}

func (s *Server) handleCommit(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	message, _ := args["message"].(string)
	if message == "" {
		message = fmt.Sprintf("wiki: update at %s", time.Now().Format("2006-01-02 15:04"))
	}
	if err := gitpkg.AutoCommit(s.projectDir, message); err != nil {
		return errorResult(fmt.Sprintf("commit failed: %v", err)), nil
	}
	return textResult(fmt.Sprintf("Committed: %s", message)), nil
}

func (s *Server) handleCompileDiff(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	mf, err := manifest.Load(filepath.Join(s.projectDir, ".manifest.json"))
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Simple diff: count pending sources
	pending := mf.PendingSources()
	total := mf.SourceCount()

	return textResult(fmt.Sprintf("Sources: %d total, %d pending compilation", total, len(pending))), nil
}

func (s *Server) tryEmbed(id string, content string) {
	embedder := embed.NewFromConfig(s.cfg)
	if embedder != nil {
		if vec, err := embedder.Embed(content); err == nil {
			s.vec.Upsert(id, vec)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
