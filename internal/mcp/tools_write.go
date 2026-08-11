package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/capturefmt"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/fsutil"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
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

	// Accept paths within the project root OR within any configured source
	// directory. Vault overlays use relative paths like ../../docs/... that
	// resolve outside the project root but are still legitimate sources
	// listed in cfg.sources (#51). Random traversal (../../etc/passwd) is
	// still rejected because it won't match any configured source.
	allowed := isSubpath(absProject, absPath)
	if !allowed {
		for _, src := range s.cfg.ResolveSources(absProject) {
			absSrc, _ := filepath.Abs(src)
			if isSubpath(absSrc, absPath) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
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

	// Manifest RMW under the exclusive lock (D4) so a concurrent compile or
	// another writer cannot clobber this source (lost update).
	if err := manifest.Mutate(ctx, filepath.Join(s.projectDir, ".manifest.json"), func(mf *manifest.Manifest) error {
		mf.AddSource(path, hash, srcType, info.Size())
		return nil
	}); err != nil {
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

	summaryPath := filepath.Join(s.cfg.Output, "summaries", compiler.SummaryFilename(source))
	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(filepath.Join(s.projectDir, summaryPath))
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return errorResult(fmt.Sprintf("create dir %s: %v", filepath.Dir(absPath), err)), nil
	}

	// Canonical write-then-index order (I2): (1) write the summary atomically,
	// (2) index into the DB (mem + vectors, below), (3) update the manifest under
	// the lock. A crash between steps leaves drift the reconciler heals.
	frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\n---\n\n", source, s.cfg.Compiler.UserNow())
	if err := fsutil.WriteFileAtomic(absPath, []byte(frontmatter+content), 0644); err != nil {
		return errorResult(fmt.Sprintf("write failed: %v", err)), nil
	}

	// Post-write index failures are logged, not returned: the file is already
	// on disk (an error result would misreport the write), and the P1-2
	// startup reconciler heals the drift. REL-04.
	if err := s.mem.Add(memory.Entry{ID: source, Content: content, ArticlePath: summaryPath}); err != nil {
		log.Warn("index summary failed (reconciler will heal)", "source", source, "error", err)
	}
	sourcedate.RecordForSource(s.mem, s.projectDir, source, "")
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

	// Manifest RMW under the exclusive lock (D4) so a concurrent compile or
	// another writer cannot clobber this summary's mark (lost update).
	if err := manifest.Mutate(ctx, filepath.Join(s.projectDir, ".manifest.json"), func(mf *manifest.Manifest) error {
		if _, exists := mf.Sources[source]; !exists {
			mf.AddSource(source, "", "article", int64(len(content)))
		}
		mf.MarkCompiled(source, summaryPath, concepts)
		return nil
	}); err != nil {
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
	// SPEC-08 AC1: concept ids are identifiers — strict charset rejection,
	// aligned with the REST edge rule and the CLI shim.
	if !pathsafe.ValidConceptID(conceptID) {
		err := fmt.Errorf("invalid concept id %q: %w", conceptID, limits.ErrInvalidName)
		s.emitLimitExceeded(err, "mcp:wiki_write_article")
		return errorResult(err.Error()), nil
	}

	articlePath := filepath.Join(s.cfg.Output, "concepts", conceptID+".md")
	absProject, _ := filepath.Abs(s.projectDir)
	absPath, _ := filepath.Abs(filepath.Join(s.projectDir, articlePath))
	if !isSubpath(absProject, absPath) {
		return errorResult("path traversal not allowed"), nil
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return errorResult(fmt.Sprintf("create dir %s: %v", filepath.Dir(absPath), err)), nil
	}

	// Canonical write-then-index order (I2): (1) write the article atomically,
	// (2) index into the DB (ontology + mem + vectors, below), (3) update the
	// manifest under the lock. A crash between steps leaves drift the reconciler heals.
	if err := fsutil.WriteFileAtomic(absPath, []byte(content), 0644); err != nil {
		return errorResult(fmt.Sprintf("write failed: %v", err)), nil
	}

	// Post-write index failures are logged, not returned (reconciler heals;
	// the article file is already on disk). REL-04.
	if err := s.ont.AddEntity(ontology.Entity{
		ID:          conceptID,
		Type:        ontology.ArticleEntityType(content, s.ont),
		Name:        ontology.FormatConceptName(conceptID),
		ArticlePath: articlePath,
	}); err != nil {
		log.Warn("index article entity failed (reconciler will heal)", "concept", conceptID, "error", err)
	}
	if err := s.mem.Add(memory.Entry{ID: "concept:" + conceptID, Content: content, ArticlePath: articlePath}); err != nil {
		log.Warn("index article failed (reconciler will heal)", "concept", conceptID, "error", err)
	}
	s.tryEmbed("concept:"+conceptID, content)

	// Manifest RMW under the exclusive lock (D4) so a concurrent compile or
	// another writer cannot clobber this concept (lost update). A concurrent
	// compile AddConcept for the same key wins on same-key merge (D3) because it
	// carries real Sources; this MCP AddConcept writes nil Sources.
	if err := manifest.Mutate(ctx, filepath.Join(s.projectDir, ".manifest.json"), func(mf *manifest.Manifest) error {
		mf.AddConcept(conceptID, articlePath, nil)
		return nil
	}); err != nil {
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
		relID := sourceID + "-" + relType + "-" + targetID
		if err := s.ont.AddRelation(ontology.Relation{
			ID: relID, SourceID: sourceID, TargetID: targetID, Relation: relType,
			ValidFrom: time.Now().UTC().Format(time.RFC3339), // manual add = asserted now (P3-6)
		}); err != nil {
			return errorResult(fmt.Sprintf("add relation failed: %v", err)), nil
		}
		msg := fmt.Sprintf("Relation: %s -[%s]-> %s", sourceID, relType, targetID)
		// P3-6: manual adds are explicit user intent — functional predicates
		// auto-apply supersession; bare contradicts edges surface a trust
		// conflict for review, same rule as the extraction path.
		if s.cfg.Ontology.Temporal.EnabledOrDefault() {
			if relType == ontology.RelContradicts {
				s.emitEdgeConflict(
					fmt.Sprintf("Edge conflict: %s contradicts %s (source: manual add)", sourceID, targetID),
					"Deferred: entity-level contradicts edge recorded for review; no auto-invalidation.")
			} else if s.functionalPredicate(relType) {
				invalidated, err := s.ont.InvalidateFunctional(sourceID, relType, targetID,
					time.Now().UTC().Format(time.RFC3339), relID)
				if err != nil {
					// The new edge is already committed and live; retrying
					// the same add re-fires supersession idempotently.
					return errorResult(fmt.Sprintf("relation added but supersession failed: %v (both values are now live; retry the add to re-apply)", err)), nil
				}
				if len(invalidated) > 0 {
					msg += fmt.Sprintf(" (superseded %d prior edge(s))", len(invalidated))
				}
			}
		}
		return textResult(msg), nil
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

// checkQueryLen enforces limits.MaxQueryBytes on query/question inputs
// (SPEC-08 D1) before any provider call. Emits limit_exceeded and returns
// the typed error; nil when within the cap.
func (s *Server) checkQueryLen(q string) error {
	lim := s.cfg.Limits.Resolve()
	if int64(len(q)) > lim.MaxQueryBytes {
		le := limits.New(limits.WhichQueryBytes, lim.MaxQueryBytes, int64(len(q)))
		s.emitLimitExceeded(le, "mcp:query")
		return le
	}
	return nil
}

// emitLimitExceeded fans one limit_exceeded event into the installed sink
// for tool-level enforcement (SPEC-08 D2 emission inventory). Detail is a
// short locator, never content. No sink = no-op.
func (s *Server) emitLimitExceeded(err error, detail string) {
	sink := s.eventSink()
	if sink == nil {
		return
	}
	var le *limits.LimitError
	if errors.As(err, &le) {
		sink.Emit(events.NewEvent(filepath.Base(s.projectDir), events.TypeLimitExceeded, events.LimitExceeded{
			Which:  le.Which,
			Limit:  le.Limit,
			Got:    le.Got,
			Detail: detail,
		}))
		return
	}
	which := limits.WhichInvalidName
	if errors.Is(err, limits.ErrEncoding) {
		which = limits.WhichEncoding
	}
	if errors.Is(err, limits.ErrInvalidName) || errors.Is(err, limits.ErrEncoding) {
		sink.Emit(events.NewEvent(filepath.Base(s.projectDir), events.TypeLimitExceeded, events.LimitExceeded{
			Which:  which,
			Detail: detail,
		}))
	}
}

func (s *Server) handleCapture(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	args := req.GetArguments()
	content, _ := args["content"].(string)
	if content == "" {
		return errorResult("content is required"), nil
	}
	if len(content) > maxCaptureSize {
		return errorResult(fmt.Sprintf("content too large (%d bytes, max %d)", len(content), maxCaptureSize)), nil
	}
	// SPEC-08 D3: wiki_capture is a TEXT surface — invalid UTF-8 is
	// rejected with the typed encoding error, never stored.
	if err := wiki.EncodingGate("capture.md", []byte(content)); err != nil {
		s.emitLimitExceeded(err, "mcp:wiki_capture")
		return errorResult(fmt.Sprintf("capture rejected: %v", err)), nil
	}

	captureCtx, _ := args["context"].(string)
	tagsStr, _ := args["tags"].(string)

	// Ensure captures directory exists
	capturesDir := filepath.Join(s.projectDir, "raw", "captures")
	if err := os.MkdirAll(capturesDir, 0755); err != nil {
		return errorResult(fmt.Sprintf("create captures dir: %v", err)), nil
	}

	// Try LLM extraction
	items, err := extractKnowledgeItems(s.cfg, s.projectDir, content, captureCtx, tagsStr)
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

	// SPEC-08 D1: per-call capture batch cap. Over-cap is a typed failure
	// with an event — nothing partial persists.
	lim := s.cfg.Limits.Resolve()
	if int64(len(items)) > lim.MaxDocsPerCaptureBatch {
		le := limits.New(limits.WhichCaptureBatch, lim.MaxDocsPerCaptureBatch, int64(len(items)))
		s.emitLimitExceeded(le, "mcp:wiki_capture")
		return errorResult(fmt.Sprintf("capture rejected: %v", le)), nil
	}

	// SPEC-08 D3 (behavior change 6): a title that sanitizes to an empty
	// slug fails the capture — the old silent timestamped fallback let
	// unnameable content in.
	for _, item := range items {
		if slugify(item.Title) == "" {
			err := fmt.Errorf("capture rejected: title %q sanitizes to an empty slug: %w", item.Title, limits.ErrInvalidName)
			s.emitLimitExceeded(err, "mcp:wiki_capture")
			return errorResult(err.Error()), nil
		}
	}

	// Write each item as a source file. The manifest RMW is deferred to one
	// locked Mutate at the end (D4): file writes are per-path and race-free, so
	// only the manifest update needs the lock, and batching keeps it to a single
	// locked pass. captureSource records what to register.
	type captureSource struct {
		relPath string
		hash    string
		size    int64
	}
	var titles []string
	var toAdd []captureSource
	usedSlugs := map[string]int{}

	for _, item := range items {
		slug := slugify(item.Title)
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

		frontmatter, err := capturefmt.Frontmatter("mcp-capture", s.cfg.Compiler.UserNow(), tagsStr, captureCtx)
		if err != nil {
			return errorResult(fmt.Sprintf("capture: %v", err)), nil
		}

		fileContent := frontmatter + "# " + item.Title + "\n\n" + item.Content + "\n"
		if err := os.WriteFile(absPath, []byte(fileContent), 0644); err != nil {
			log.Warn("capture: write failed", "path", relPath, "error", err)
			continue
		}

		// Defer the manifest registration to the single locked Mutate below.
		hash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fileContent)))
		toAdd = append(toAdd, captureSource{relPath: relPath, hash: hash, size: int64(len(fileContent))})
		titles = append(titles, item.Title)
	}

	if len(toAdd) > 0 {
		// Best-effort, as before: a failed manifest update leaves the source
		// files on disk, which compiler.Diff still discovers on the next run.
		if err := manifest.Mutate(ctx, filepath.Join(s.projectDir, ".manifest.json"), func(mf *manifest.Manifest) error {
			for _, a := range toAdd {
				mf.AddSource(a.relPath, a.hash, "article", a.size)
			}
			return nil
		}); err != nil {
			log.Warn("capture: manifest update failed", "error", err)
		}
	}

	return textResult(fmt.Sprintf("Captured %d items: %s\nRun `wiki_compile` to process them into articles.", len(titles), strings.Join(titles, ", "))), nil
}

type capturedItem struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

func extractKnowledgeItems(cfg *config.Config, projectDir string, content, captureCtx, tags string) ([]capturedItem, error) {
	if cfg.API.Provider == "" {
		return nil, fmt.Errorf("LLM not configured (no api.provider)")
	}
	if cfg.API.APIKey == "" && cfg.API.Auth != "subscription" {
		return nil, fmt.Errorf("LLM not configured (no api.api_key — set api_key or use api.auth: subscription)")
	}

	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}
	// SPEC-05 usage ledger: capture-extraction spend is recorded.
	client.SetRecorder(llm.NewFileRecorder(projectDir))
	client.SetPass("extract")
	client.SetPriceOverride(cfg.Compiler.TokenPriceOverride)
	client.SetPriceTable(cfg.Compiler.PriceTable)

	// Per-workspace template registry (SPEC-01): the MCP server is bound to
	// one projectDir, so its prompt overrides must not depend on whatever
	// the process-global default last loaded.
	registry := prompts.NewRegistry()
	if err := registry.LoadFromDir(filepath.Join(projectDir, "prompts")); err != nil {
		return nil, fmt.Errorf("load prompt overrides: %w", err)
	}
	prompt, err := registry.Render("capture_knowledge", prompts.CaptureData{
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

	// P2-4: schema-guaranteed JSON where the provider supports it;
	// RawFallback keeps this site's exact no-bracket-hunt parse tolerance.
	payload, rawText, err := client.StructuredCompletion(context.Background(), []llm.Message{
		{Role: "system", Content: "You are a knowledge extraction assistant. Return only valid JSON."},
		{Role: "user", Content: prompt + "\n\n" + prompts.WrapUntrusted(content)},
	}, CaptureSchema, llm.CallOpts{Model: model, MaxTokens: 4096, RawFallback: true})
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	// Strip markdown code fences if present (fallback path only)
	text := string(payload)
	if rawText != "" {
		text = strings.TrimSpace(rawText)
		text = stripJSONFences(text)
	}

	var items []capturedItem
	if err := json.Unmarshal([]byte(text), &items); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w (raw: %s)", err, truncate(text, 200))
	}

	return items, nil
}

func writeRawCapture(projectDir, content, captureCtx, tags, userNow string) (string, error) {
	capturesDir := filepath.Join(projectDir, "raw", "captures")
	if err := os.MkdirAll(capturesDir, 0755); err != nil {
		return "", fmt.Errorf("create captures dir: %w", err)
	}

	slug := fmt.Sprintf("raw-%s", time.Now().Format("20060102-150405"))
	relPath := filepath.Join("raw", "captures", slug+".md")
	absPath := filepath.Join(projectDir, relPath)

	// Defense-in-depth: verify path stays within project
	absProject, _ := filepath.Abs(projectDir)
	absChecked, _ := filepath.Abs(absPath)
	if !isSubpath(absProject, absChecked) {
		return "", fmt.Errorf("path traversal blocked: %s", relPath)
	}

	frontmatter, err := capturefmt.Frontmatter("mcp-capture", userNow, tags, captureCtx, "raw: true")
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}

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
	mf.SetNow(config.NowUTC)

	// Run the real diff: walk configured source directories, compute hashes,
	// and compare against the manifest. Previously this handler only counted
	// manifest entries with status "pending", so new files on disk that
	// hadn't been ingested yet were completely invisible to MCP clients
	// (issue #51).
	diff, err := compiler.Diff(s.projectDir, s.cfg, mf)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	total := mf.SourceCount()
	pending := len(diff.Added) + len(diff.Modified)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Sources: %d total, %d pending compilation\n", total, pending)
	fmt.Fprintf(&sb, "Added: %d, Modified: %d, Removed: %d\n",
		len(diff.Added), len(diff.Modified), len(diff.Removed))

	if len(diff.Added) > 0 {
		sb.WriteString("\nNew files:\n")
		for _, f := range diff.Added {
			fmt.Fprintf(&sb, "  + %s (%d bytes)\n", f.Path, f.Size)
		}
	}
	if len(diff.Modified) > 0 {
		sb.WriteString("\nModified files:\n")
		for _, f := range diff.Modified {
			fmt.Fprintf(&sb, "  ~ %s (%d bytes)\n", f.Path, f.Size)
		}
	}
	if len(diff.Removed) > 0 {
		sb.WriteString("\nRemoved files:\n")
		for _, p := range diff.Removed {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
	}

	return textResult(sb.String()), nil
}

// tryEmbed embeds content into the vector store using the server's shared
// embedder (built once in NewServer — re-detecting per call would re-probe
// providers and warn-spam offline deployments on every write).
func (s *Server) tryEmbed(id string, content string) {
	if s.embedder != nil {
		vec, err := s.embedder.Embed(content)
		if err != nil {
			// A configured-but-failing embedder must be visible (REL-04);
			// unconfigured embedders short-circuit above and stay silent.
			log.Warn("embed failed (reconciler will heal)", "id", id, "error", err)
			return
		}
		if err := s.vec.Upsert(id, vec); err != nil {
			log.Warn("vector upsert failed (reconciler will heal)", "id", id, "error", err)
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

	result, err := s.CompileTopic(ctx, topic, maxSources)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return textResult(string(data)), nil
}

// CompileTopic runs compile-on-demand against the server's stores. Exported
// so the REST job runner (P4-2) shares the exact MCP wiring.
func (s *Server) CompileTopic(ctx context.Context, topic string, maxSources int) (*compiler.OnDemandResult, error) {
	cfg, err := config.Load(filepath.Join(s.projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load config: %v", err)
	}

	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %v", err)
	}
	// SPEC-05 usage ledger: compile-on-demand spend is recorded —
	// bridged to the workspace event sink when one is installed (SPEC-07).
	client.SetRecorder(llm.NewBridgedRecorder(s.projectDir, s.eventSink()))
	client.SetTier(3)
	client.SetPriceOverride(cfg.Compiler.TokenPriceOverride)
	client.SetPriceTable(cfg.Compiler.PriceTable)

	// R3/R5: each on-demand request loads a FRESH project registry (embedded
	// defaults + <projectDir>/prompts), so overrides never depend on package-
	// global state and multi-workspace serves cannot cross-contaminate. A
	// missing prompts/ dir stays silent; a malformed override warns and keeps
	// the defaults (R4) — prompt loading never aborts topic compilation,
	// mirroring the engine's warning-and-default behavior rather than the
	// neighboring wiki_capture hard-error path.
	registry := prompts.NewRegistry()
	if err := registry.LoadFromDir(filepath.Join(s.projectDir, "prompts")); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}

	result, err := compiler.CompileTopic(ctx, compiler.OnDemandOpts{
		Topic:       topic,
		MaxSources:  maxSources,
		ProjectDir:  s.projectDir,
		Config:      cfg,
		DB:          s.db,
		TrustStore:  s.trustStore(),
		Searcher:    s.searcher,
		Embedder:    s.embedder,
		Client:      client,
		Coordinator: s.coordinator,
		Prompts:     registry,
		Sink:        s.eventSink(),
	})
	if err != nil {
		return nil, fmt.Errorf("compile topic: %v", err)
	}
	return result, nil
}

// CaptureSchema is the canonical schema for wiki_capture parsing (P2-4).
var CaptureSchema = llm.JSONSchema{
	Name:        "capture",
	Description: "knowledge items captured from raw text",
	IsArray:     true,
	Schema: map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"title", "content"},
		},
		"minItems": 0,
	},
}

// trustStore returns the Backend's Trust store when one is wired (the
// postgres path), falling back to the sqlite implementation over the raw
// handle. trust.NewStore emits '?' placeholders and silently fails under a
// Postgres backend (Gate-3 i2).
func (s *Server) trustStore() store.TrustStore {
	if s.backend != nil {
		return s.backend.Trust()
	}
	return trust.NewStore(s.db)
}

// functionalPredicate reports whether relType is configured functional
// (outbound uniqueness, P3-6) in either relation config key.
func (s *Server) functionalPredicate(relType string) bool {
	// config.Load normalizes relation_types into Relations — one loop only.
	for _, rc := range s.cfg.Ontology.Relations {
		if rc.Name == relType && rc.Functional {
			return true
		}
	}
	return false
}

// emitEdgeConflict records a trust conflict for a manually added contradicts
// edge (P3-6). Deterministic ID dedups repeats; insert races lose to the PK
// and are swallowed — conflict surfacing is best-effort.
func (s *Server) emitEdgeConflict(question, answer string) {
	ts := s.trustStore()
	sum := sha256.Sum256([]byte(question))
	id := "edgeconflict-" + hex.EncodeToString(sum[:])[:16]
	if existing, err := ts.Get(id); err == nil && existing != nil {
		return
	}
	o := &store.PendingOutput{
		ID:           id,
		Question:     question,
		QuestionHash: trust.HashQuestion(question),
		Answer:       answer,
		AnswerHash:   trust.HashAnswer(answer),
		State:        store.StateConflict,
		SourcesUsed:  "[]",
		SourcesHash:  trust.ComputeSourcesHash("", "[]"),
		CreatedAt:    time.Now().UTC(),
	}
	if err := ts.InsertPending(o); err != nil {
		log.Debug("wiki_ontology_add: edge conflict insert skipped", "id", id, "error", err)
	}
}
