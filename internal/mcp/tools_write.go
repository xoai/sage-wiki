package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
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

	s.mcp.AddTool(
		mcplib.NewTool("wiki_capture",
			mcplib.WithDescription("Capture knowledge from a conversation or text. Extracts key learnings via LLM and stores them as wiki sources for compilation."),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Conversation excerpt or text to extract knowledge from (max 100KB)")),
			mcplib.WithString("context", mcplib.Description("What the conversation was about")),
			mcplib.WithString("tags", mcplib.Description("Comma-separated tags for captured items")),
		),
		s.handleCapture,
	)

	s.mcp.AddTool(
		mcplib.NewTool("wiki_compile_topic",
			mcplib.WithDescription("Compile sources for a specific topic on demand. Finds uncompiled sources matching the topic, promotes them, and runs the full compilation pipeline. Use when wiki_search returns uncompiled_sources > 0."),
			mcplib.WithString("topic", mcplib.Required(), mcplib.Description("Topic or query to compile sources for")),
			mcplib.WithNumber("max_sources", mcplib.Description("Maximum sources to compile (default 20)")),
		),
		s.handleCompileTopic,
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

	frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\n---\n\n", source, s.cfg.Compiler.UserNow())
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

const maxCaptureSize = 100 * 1024 // 100KB

func (s *Server) handleCapture(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	content, _ := args["content"].(string)
	if content == "" {
		return errorResult("content is required"), nil
	}
	if len(content) > maxCaptureSize {
		return errorResult(fmt.Sprintf("content too large (%d bytes, max %d)", len(content), maxCaptureSize)), nil
	}

	captureCtx, _ := args["context"].(string)
	tagsStr, _ := args["tags"].(string)

	// Ensure captures directory exists
	capturesDir := filepath.Join(s.projectDir, "raw", "captures")
	if err := os.MkdirAll(capturesDir, 0755); err != nil {
		return errorResult(fmt.Sprintf("create captures dir: %v", err)), nil
	}

	// Try LLM extraction
	items, err := extractKnowledgeItems(s.cfg, content, captureCtx, tagsStr)
	if err != nil {
		// Fallback: store raw content as single file
		log.Warn("capture: LLM extraction failed, storing raw", "error", err)
		path, writeErr := writeRawCapture(s.projectDir, content, captureCtx, tagsStr, s.cfg.Compiler.UserNow())
		if writeErr != nil {
			return errorResult(fmt.Sprintf("write failed: %v", writeErr)), nil
		}
		return textResult(fmt.Sprintf("LLM extraction failed (%v). Raw content saved to %s", err, path)), nil
	}

	if len(items) == 0 {
		return textResult("No knowledge items found worth extracting."), nil
	}

	// Write each item as a source file
	var titles []string
	mf, _ := manifest.Load(filepath.Join(s.projectDir, ".manifest.json"))
	usedSlugs := map[string]int{}

	for _, item := range items {
		slug := slugify(item.Title)
		if slug == "" {
			slug = fmt.Sprintf("capture-%d", time.Now().UnixNano())
		}
		// Disambiguate duplicate slugs
		if n, exists := usedSlugs[slug]; exists {
			usedSlugs[slug] = n + 1
			slug = fmt.Sprintf("%s-%d", slug, n+1)
		} else {
			usedSlugs[slug] = 1
		}
		relPath := filepath.Join("raw", "captures", slug+".md")
		absPath := filepath.Join(s.projectDir, relPath)

		// Defense-in-depth: verify path stays within project
		absProject, _ := filepath.Abs(s.projectDir)
		absChecked, _ := filepath.Abs(absPath)
		if !isSubpath(absProject, absChecked) {
			log.Warn("capture: path traversal blocked", "slug", slug)
			continue
		}

		frontmatter := fmt.Sprintf("---\nsource: mcp-capture\ncaptured_at: %s\n", s.cfg.Compiler.UserNow())
		if tagsStr != "" {
			frontmatter += fmt.Sprintf("tags: [%s]\n", tagsStr)
		}
		if captureCtx != "" {
			frontmatter += fmt.Sprintf("context: %q\n", captureCtx)
		}
		frontmatter += "---\n\n"

		fileContent := frontmatter + "# " + item.Title + "\n\n" + item.Content + "\n"
		if err := os.WriteFile(absPath, []byte(fileContent), 0644); err != nil {
			log.Warn("capture: write failed", "path", relPath, "error", err)
			continue
		}

		// Update manifest
		if mf != nil {
			hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fileContent)))
			mf.AddSource(relPath, hash, "article", int64(len(fileContent)))
		}
		titles = append(titles, item.Title)
	}

	if mf != nil {
		if err := mf.Save(filepath.Join(s.projectDir, ".manifest.json")); err != nil {
			log.Warn("capture: manifest save failed", "error", err)
		}
	}

	return textResult(fmt.Sprintf("Captured %d items: %s\nRun `wiki_compile` to process them into articles.", len(titles), strings.Join(titles, ", "))), nil
}

type capturedItem struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func extractKnowledgeItems(cfg *config.Config, content, captureCtx, tags string) ([]capturedItem, error) {
	if cfg.API.Provider == "" || cfg.API.APIKey == "" {
		return nil, fmt.Errorf("LLM not configured (no api.provider or api.api_key)")
	}

	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit, cfg.API.ExtraParams)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}

	prompt, err := prompts.Render("capture_knowledge", prompts.CaptureData{
		Context: captureCtx,
		Tags:    tags,
	}, cfg.Language)
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are a knowledge extraction assistant. Return only valid JSON."},
		{Role: "user", Content: prompt + "\n\n" + content},
	}, llm.CallOpts{Model: model, MaxTokens: 4096})
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Strip markdown code fences if present
	text := strings.TrimSpace(resp.Content)
	text = stripJSONFences(text)

	var items []capturedItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w (raw: %s)", err, truncate(text, 200))
	}

	return items, nil
}

func writeRawCapture(projectDir, content, captureCtx, tags, userNow string) (string, error) {
	capturesDir := filepath.Join(projectDir, "raw", "captures")
	os.MkdirAll(capturesDir, 0755)

	slug := fmt.Sprintf("raw-%s", time.Now().Format("20060102-150405"))
	relPath := filepath.Join("raw", "captures", slug+".md")
	absPath := filepath.Join(projectDir, relPath)

	// Defense-in-depth: verify path stays within project
	absProject, _ := filepath.Abs(projectDir)
	absChecked, _ := filepath.Abs(absPath)
	if !isSubpath(absProject, absChecked) {
		return "", fmt.Errorf("path traversal blocked: %s", relPath)
	}

	frontmatter := fmt.Sprintf("---\nsource: mcp-capture\ncaptured_at: %s\nraw: true\n", userNow)
	if tags != "" {
		frontmatter += fmt.Sprintf("tags: [%s]\n", tags)
	}
	if captureCtx != "" {
		frontmatter += fmt.Sprintf("context: %q\n", captureCtx)
	}
	frontmatter += "---\n\n"

	if err := os.WriteFile(absPath, []byte(frontmatter+content+"\n"), 0644); err != nil {
		return "", err
	}
	return relPath, nil
}

var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	// Keep ASCII letters/digits, replace everything else with hyphens
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf.WriteRune(r)
		} else {
			buf.WriteByte('-')
		}
	}
	s = nonAlphanumRe.ReplaceAllString(buf.String(), "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = s[:80]
		s = strings.TrimRight(s, "-")
	}
	return s
}

func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
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

func (s *Server) handleCompileTopic(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	topic, _ := args["topic"].(string)
	if topic == "" {
		return errorResult("topic is required"), nil
	}

	maxSources := 20
	if ms, ok := args["max_sources"].(float64); ok && ms > 0 {
		maxSources = int(ms)
	}

	cfg, err := config.Load(filepath.Join(s.projectDir, "config.yaml"))
	if err != nil {
		return errorResult(fmt.Sprintf("load config: %v", err)), nil
	}

	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit, cfg.API.ExtraParams)
	if err != nil {
		return errorResult(fmt.Sprintf("create LLM client: %v", err)), nil
	}

	result, err := compiler.CompileTopic(ctx, compiler.OnDemandOpts{
		Topic:       topic,
		MaxSources:  maxSources,
		ProjectDir:  s.projectDir,
		Config:      cfg,
		DB:          s.db,
		Searcher:    s.searcher,
		Embedder:    s.embedder,
		Client:      client,
		Coordinator: s.coordinator,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("compile topic: %v", err)), nil
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(data)), nil
}
