package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// CompileOpts configures a compilation run.
type CompileOpts struct {
	DryRun   bool
	Fresh    bool              // ignore checkpoint
	Batch    bool              // use batch API (async, 50% discount)
	NoCache  bool              // disable prompt caching
	Prune    bool              // delete orphaned articles when sources removed
	Tracker  *llm.CostTracker  // optional cost tracker
}

// CompileResult summarizes what happened during compilation.
type CompileResult struct {
	Added              int
	Modified           int
	Removed            int
	Summarized         int
	ConceptsExtracted  int
	ArticlesWritten    int
	Errors             int
	EmbedErrors        int
	CostReport         *llm.CostReport // nil if no LLM calls were made
	TierIndexed        int             // sources indexed at Tier 0
	TierEmbedded       int             // sources embedded at Tier 1
	TierCompiled       int             // sources sent through full pipeline (Tier 3)
}

// CompileState tracks progress for checkpoint/resume (ADR-018).
type CompileState struct {
	CompileID string   `json:"compile_id"`
	StartedAt string   `json:"started_at"`
	Pass      int      `json:"pass"`
	Completed []string `json:"completed"`
	Pending   []string `json:"pending"`
	Failed    []FailedSource `json:"failed,omitempty"`
	Batch     *BatchState    `json:"batch,omitempty"` // non-nil when batch is in flight
}

// BatchState tracks an in-flight batch job for checkpoint/resume.
type BatchState struct {
	BatchID    string `json:"batch_id"`
	Provider   string `json:"provider"`
	Pass       string `json:"pass"`        // which compiler pass (summarize, extract)
	ResultsRef string `json:"results_ref"` // Anthropic: results URL; OpenAI: output_file_id
	SubmittedAt string `json:"submitted_at"`
}

type FailedSource struct {
	Path     string `json:"path"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

// Compile runs Pass 0 (diff) and Pass 1 (summarize) of the compiler pipeline.
func Compile(projectDir string, opts CompileOpts) (*CompileResult, error) {
	result := &CompileResult{}

	// Load config
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("compile: load config: %w", err)
	}

	// Load user prompt overrides if prompts/ directory exists
	promptsDir := filepath.Join(projectDir, "prompts")
	if err := prompts.LoadFromDir(promptsDir); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}

	// Load manifest
	mfPath := filepath.Join(projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return nil, fmt.Errorf("compile: load manifest: %w", err)
	}

	// Check for existing checkpoint
	statePath := filepath.Join(projectDir, ".sage", "compile-state.json")
	var state *CompileState
	if !opts.Fresh {
		state, _ = loadCompileState(statePath)
		if state != nil {
			log.Info("resuming from checkpoint", "compile_id", state.CompileID, "pass", state.Pass, "completed", len(state.Completed))
		}
	}

	// Pass 0: Diff
	log.Info("Pass 0: computing diff")
	diff, err := Diff(projectDir, cfg, mf)
	if err != nil {
		return nil, fmt.Errorf("compile: diff: %w", err)
	}

	result.Added = len(diff.Added)
	result.Modified = len(diff.Modified)
	result.Removed = len(diff.Removed)

	progress := NewProgress()

	if result.Added == 0 && result.Modified == 0 && result.Removed == 0 {
		fmt.Fprintln(os.Stderr, "✓ Nothing to compile — wiki is up to date.")
		return result, nil
	}

	if opts.DryRun {
		fmt.Fprintln(os.Stderr, "Dry run — changes that would be applied:")
		for _, s := range diff.Added {
			fmt.Fprintf(os.Stderr, "  + %s (%s)\n", s.Path, s.Type)
		}
		for _, s := range diff.Modified {
			fmt.Fprintf(os.Stderr, "  ~ %s (%s)\n", s.Path, s.Type)
		}
		for _, p := range diff.Removed {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		return result, nil
	}

	// Create LLM client
	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit, cfg.API.ExtraParams)
	if err != nil {
		return nil, fmt.Errorf("compile: create LLM client: %w", err)
	}

	// Attach cost tracker (with optional price override from config)
	tracker := opts.Tracker
	if tracker == nil {
		tracker = llm.NewCostTracker(cfg.API.Provider, cfg.Compiler.TokenPriceOverride)
	}
	client.SetTracker(tracker)

	// Check for pending batch to resume
	if state != nil && state.Batch != nil {
		if client.ProviderName() != state.Batch.Provider {
			return nil, fmt.Errorf("compile: provider changed from %s to %s since batch was submitted — clear checkpoint with --fresh or switch back", state.Batch.Provider, client.ProviderName())
		}
		return resumeBatch(projectDir, client, cfg, mf, state, statePath, tracker, opts)
	}

	// Resolve batch mode: CLI flag > config mode > default (standard)
	useBatch := opts.Batch
	if !useBatch && cfg.Compiler.Mode == "batch" {
		useBatch = true
	}
	if !useBatch && cfg.Compiler.Mode == "auto" && client.SupportsBatch() {
		sourceCount := len(diff.Added) + len(diff.Modified)
		threshold := cfg.Compiler.BatchThreshold
		if threshold <= 0 {
			threshold = 10 // default: auto-batch when 10+ sources
		}
		if sourceCount >= threshold {
			useBatch = true
			log.Info("auto-selecting batch mode", "sources", sourceCount, "threshold", threshold)
		}
	}
	if useBatch {
		if !client.SupportsBatch() {
			return nil, fmt.Errorf("compile: provider %s does not support batch API", cfg.API.Provider)
		}
		return submitBatch(projectDir, client, cfg, mf, diff, statePath, tracker)
	}

	// Open DB
	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("compile: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	pipelineOntStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))

	// Backfill chunk index if needed (after migration, before first compile)
	if chunkStore.NeedsBackfill(memStore) {
		log.Info("chunk index empty with existing articles — running backfill")
		if err := BackfillChunks(projectDir, cfg.Output, cfg.Search.ChunkSizeOrDefault(), chunkStore, vecStore, embedder, db); err != nil {
			log.Warn("chunk backfill failed", "error", err)
		}
	}

	// Initialize compile_items store and tier manager
	itemStore := NewCompileItemStore(db)
	tierMgr := NewTierManager(&cfg.Compiler, itemStore)
	bp := NewBackpressureController(cfg.Compiler.MaxParallel)

	// Populate compile_items from manifest on first run (if empty)
	if count, _ := itemStore.Count(); count == 0 && mf.SourceCount() > 0 {
		populated, err := PopulateFromManifest(db, mf, cfg)
		if err != nil {
			log.Warn("populate compile_items from manifest failed", "error", err)
		} else if populated > 0 {
			log.Info("populated compile_items from manifest", "count", populated)
		}
	}

	// Migrate legacy checkpoint if present
	if !opts.Fresh {
		if migrated, err := MigrateCheckpoint(projectDir, db, mf, cfg); err != nil {
			log.Warn("checkpoint migration failed", "error", err)
		} else if migrated {
			log.Info("legacy checkpoint migrated to compile_items")
		}
	}

	// Initialize legacy checkpoint state (retained for fallback)
	if state == nil {
		state = &CompileState{
			CompileID: time.Now().Format("20060102-150405"),
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Pass:      1,
		}
	}

	// Resolve tiers and upsert compile_items for new/modified sources
	allSources := append(diff.Added, diff.Modified...)
	compileID := state.CompileID
	for _, src := range allSources {
		tier := tierMgr.ResolveTier(src.Path, projectDir, nil)
		itemStore.Upsert(CompileItem{
			SourcePath:  src.Path,
			Hash:        src.Hash,
			FileType:    src.Type,
			SizeBytes:   src.Size,
			Tier:        tier,
			TierDefault: tierMgr.ConfigDefault(src.Path),
			SourceType:  "compiler",
			CompileID:   compileID,
		})
	}

	// Tier 0: FTS5 index only (no LLM, ~5ms/doc)
	tier0Pending, _ := itemStore.ListPending(0)
	if len(tier0Pending) > 0 {
		progress.StartPhase("Tier 0: Index sources", len(tier0Pending))
		indexed := indexRawSources(projectDir, tier0Pending, memStore, itemStore)
		result.TierIndexed = indexed
		log.Info("tier 0 indexing complete", "indexed", indexed)
		progress.EndPhase()
	}

	// Tier 1: FTS5 + vector embed (~200ms/doc)
	tier1Pending, _ := itemStore.ListPending(1)
	if len(tier1Pending) > 0 {
		progress.StartPhase("Tier 1: Index + embed sources", len(tier1Pending))
		indexed, embedded := indexAndEmbedSources(projectDir, tier1Pending, memStore, vecStore, embedder, itemStore, bp)
		result.TierIndexed += indexed
		result.TierEmbedded = embedded
		log.Info("tier 1 indexing complete", "indexed", indexed, "embedded", embedded)
		progress.EndPhase()
	}

	// Tier 3: Full LLM pipeline (Pass 1 → 2 → 3) — only for Tier 3 sources
	tier3Pending, _ := itemStore.ListPending(3)
	var toProcess []SourceInfo
	tier3Set := make(map[string]bool)
	for _, item := range tier3Pending {
		tier3Set[item.SourcePath] = true
	}
	for _, s := range allSources {
		if tier3Set[s.Path] {
			toProcess = append(toProcess, s)
		}
	}

	// Also include sources from legacy checkpoint pending list
	completedSet := make(map[string]bool)
	for _, p := range state.Completed {
		completedSet[p] = true
	}
	pendingSet := make(map[string]bool)
	for _, p := range state.Pending {
		pendingSet[p] = true
	}
	for _, s := range allSources {
		if !completedSet[s.Path] && !pendingSet[s.Path] && !tier3Set[s.Path] {
			// Check if this source should be in the legacy pending list
			item, _ := itemStore.GetByPath(s.Path)
			if item != nil && item.Tier >= 3 && !item.PassWritten {
				state.Pending = append(state.Pending, s.Path)
				if !tier3Set[s.Path] {
					toProcess = append(toProcess, s)
					tier3Set[s.Path] = true
				}
			}
		}
	}

	if len(toProcess) > 0 {
		cacheEnabled := cfg.Compiler.PromptCacheEnabled() && !opts.NoCache
		pipelineResult := runFullPipeline(toProcess, FullPipelineOpts{
			ProjectDir:   projectDir,
			Config:       cfg,
			Client:       client,
			Manifest:     mf,
			DB:           db,
			MemStore:     memStore,
			VecStore:     vecStore,
			ChunkStore:   chunkStore,
			OntStore:     pipelineOntStore,
			Embedder:     embedder,
			Backpressure: bp,
			ItemStore:    itemStore,
			CacheEnabled: cacheEnabled,
			Progress:     progress,
			State:        state,
			StatePath:    statePath,
		})
		result.Summarized = pipelineResult.Summarized
		result.ConceptsExtracted = pipelineResult.ConceptsExtracted
		result.ArticlesWritten = pipelineResult.ArticlesWritten
		result.Errors += pipelineResult.Errors
		result.EmbedErrors = pipelineResult.EmbedErrors
		result.TierCompiled = len(toProcess)

		// Mark Tier 3 passes only for sources that succeeded
		succeeded := make(map[string]bool)
		for _, p := range pipelineResult.SucceededSources {
			succeeded[p] = true
		}
		for _, s := range toProcess {
			if succeeded[s.Path] {
				if err := itemStore.MarkPass(s.Path, "summarized"); err != nil {
					log.Warn("mark pass failed", "path", s.Path, "pass", "summarized", "error", err)
				}
				if err := itemStore.MarkPass(s.Path, "extracted"); err != nil {
					log.Warn("mark pass failed", "path", s.Path, "pass", "extracted", "error", err)
				}
				if err := itemStore.MarkPass(s.Path, "written"); err != nil {
					log.Warn("mark pass failed", "path", s.Path, "pass", "written", "error", err)
				}
			}
		}
	}

	// Check promotions/demotions
	if cfg.Compiler.AutoPromoteEnabled() {
		if promoted, err := tierMgr.CheckPromotions(); err == nil && len(promoted) > 0 {
			log.Info("sources eligible for promotion", "count", len(promoted))
			for _, p := range promoted {
				if err := itemStore.SetTier(p, 3, "auto-promote"); err != nil {
					log.Warn("set tier failed", "path", p, "tier", 3, "error", err)
				}
			}
		}
	}
	if cfg.Compiler.AutoDemoteEnabled() {
		if demoted, err := tierMgr.CheckDemotions(); err == nil && len(demoted) > 0 {
			log.Info("demoting stale sources", "count", len(demoted))
			for _, p := range demoted {
				if err := itemStore.SetTier(p, 1, "stale"); err != nil {
					log.Warn("set tier failed", "path", p, "tier", 1, "error", err)
				}
			}
		}
	}

	// Pass 4: Image extraction (placeholder)
	ExtractImages(projectDir, cfg.Output, toProcess)

	// Handle removed sources — detect orphans BEFORE removing from manifest
	handleRemovedSources(projectDir, diff.Removed, mf, memStore, vecStore, pipelineOntStore, opts.Prune)

	// Save manifest
	if err := mf.Save(mfPath); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	}

	// Write CHANGELOG entry
	if err := writeChangelog(projectDir, cfg.Output, result, cfg.Compiler.UserTimeLocation()); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// FTS/vector consistency check
	if result.EmbedErrors > 0 {
		ftsCount, _ := memStore.Count()
		vecCount, _ := vecStore.Count()
		if ftsCount != vecCount {
			log.Warn("FTS/vector mismatch after compile", "fts", ftsCount, "vec", vecCount, "embed_errors", result.EmbedErrors)
		}
	}

	// Clean up checkpoint on success
	if result.Errors == 0 {
		os.Remove(statePath)
	} else {
		saveCompileState(statePath, state)
	}

	// Git auto-commit
	if cfg.Compiler.AutoCommit {
		commitMsg := fmt.Sprintf("compile: +%d sources, %d concepts, %d articles",
			result.Added, result.ConceptsExtracted, result.ArticlesWritten)
		gitpkg.AutoCommit(projectDir, commitMsg)
	}

	progress.Summary(result)

	// Print cost report
	costReport := tracker.Report()
	if costReport.TotalTokens > 0 {
		fmt.Fprint(os.Stderr, llm.FormatReport(costReport))
		result.CostReport = costReport
	}

	return result, nil
}

// submitBatch builds batch requests from the diff and submits them.
// Saves checkpoint and exits — next `compile` will resume via resumeBatch.
func submitBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	diff *DiffResult,
	statePath string,
	tracker *llm.CostTracker,
) (*CompileResult, error) {
	result := &CompileResult{
		Added:    len(diff.Added),
		Modified: len(diff.Modified),
		Removed:  len(diff.Removed),
	}

	toProcess := append(diff.Added, diff.Modified...)
	if len(toProcess) == 0 {
		fmt.Fprintln(os.Stderr, "✓ Nothing to batch — wiki is up to date.")
		return result, nil
	}

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxTokens := cfg.Compiler.SummaryMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	// Build batch requests — extract content and render prompts
	var requests []llm.BatchRequest
	for _, src := range toProcess {
		absPath := filepath.Join(projectDir, src.Path)
		content, err := extract.Extract(absPath, src.Type)
		if err != nil {
			log.Warn("batch: skip source (extract failed)", "path", src.Path, "error", err)
			continue
		}

		// Skip image sources — batch doesn't support vision
		if extract.IsImageSource(content) {
			log.Info("batch: skip image source (requires vision)", "path", src.Path)
			continue
		}

		extract.ChunkIfNeeded(content, maxTokens*2)

		// Only batch single-chunk sources — multi-chunk requires sequential synthesis
		if content.ChunkCount > 1 {
			log.Info("batch: skip multi-chunk source (requires synthesis)", "path", src.Path, "chunks", content.ChunkCount)
			continue
		}

		templateName := "summarize_" + content.Type
		if _, err := prompts.Render(templateName, prompts.SummarizeData{}, ""); err != nil {
			templateName = "summarize_article"
		}

		prompt, err := prompts.Render(templateName, prompts.SummarizeData{
			SourcePath: src.Path,
			SourceType: content.Type,
			MaxTokens:  maxTokens,
		}, cfg.Language)
		if err != nil {
			log.Warn("batch: skip source (prompt render failed)", "path", src.Path, "error", err)
			continue
		}

		requests = append(requests, llm.BatchRequest{
			CustomID: src.Path,
			Messages: []llm.Message{
				{Role: "system", Content: "You are a research assistant creating structured summaries for a personal knowledge wiki."},
				{Role: "user", Content: prompt + "\n\n---\n\nSource content:\n\n" + content.Text},
			},
			Opts: llm.CallOpts{Model: model, MaxTokens: maxTokens},
		})
	}

	if len(requests) == 0 {
		fmt.Fprintln(os.Stderr, "✓ No sources eligible for batch processing.")
		return result, nil
	}

	log.Info("submitting batch", "sources", len(requests), "provider", client.ProviderName())
	batchID, err := client.SubmitBatch(requests)
	if err != nil {
		return nil, fmt.Errorf("compile: submit batch: %w", err)
	}

	// Build pending list
	var pending []string
	for _, r := range requests {
		pending = append(pending, r.CustomID)
	}

	// Save checkpoint
	utcNow := time.Now().UTC().Format(time.RFC3339)
	state := &CompileState{
		CompileID: time.Now().Format("20060102-150405"),
		StartedAt: utcNow,
		Pass:      1,
		Pending:   pending,
		Batch: &BatchState{
			BatchID:     batchID,
			Provider:    client.ProviderName(),
			Pass:        "summarize",
			SubmittedAt: utcNow,
		},
	}
	if err := saveCompileState(statePath, state); err != nil {
		return nil, fmt.Errorf("compile: CRITICAL — batch %s submitted but checkpoint save failed: %w (batch ID may be lost)", batchID, err)
	}

	fmt.Fprintf(os.Stderr, "\n📦 Batch submitted: %s\n", batchID)
	fmt.Fprintf(os.Stderr, "   Provider: %s\n", client.ProviderName())
	fmt.Fprintf(os.Stderr, "   Sources:  %d\n", len(requests))
	fmt.Fprintf(os.Stderr, "   Run `sage-wiki compile` again to check status and retrieve results.\n\n")

	return result, nil
}

// resumeBatch polls and retrieves a previously submitted batch, then continues the pipeline.
func resumeBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	state *CompileState,
	statePath string,
	tracker *llm.CostTracker,
	opts CompileOpts,
) (*CompileResult, error) {
	result := &CompileResult{}
	bs := state.Batch

	log.Info("checking batch status", "batch_id", bs.BatchID, "provider", bs.Provider)
	status, err := client.PollBatch(bs.BatchID)
	if err != nil {
		return nil, fmt.Errorf("compile: poll batch: %w", err)
	}

	switch status.Status {
	case llm.BatchInProgress:
		fmt.Fprintf(os.Stderr, "⏳ Batch %s is still processing.\n", bs.BatchID)
		fmt.Fprintln(os.Stderr, "   Run `sage-wiki compile` again later to check.")
		return result, nil

	case llm.BatchExpired:
		log.Warn("batch expired, clearing checkpoint", "batch_id", bs.BatchID)
		fmt.Fprintf(os.Stderr, "⚠ Batch %s expired (24h window). Re-run with `compile --batch` to resubmit.\n", bs.BatchID)
		state.Batch = nil
		if err := saveCompileState(statePath, state); err != nil {
			log.Warn("failed to save checkpoint after batch expiry", "error", err)
		}
		return result, nil

	case llm.BatchFailed:
		log.Error("batch failed", "batch_id", bs.BatchID)
		fmt.Fprintf(os.Stderr, "✗ Batch %s failed. Re-run with `compile --batch` to resubmit.\n", bs.BatchID)
		state.Batch = nil
		if err := saveCompileState(statePath, state); err != nil {
			log.Warn("failed to save checkpoint after batch failure", "error", err)
		}
		return result, nil

	case llm.BatchEnded:
		// Retrieve results below
	}

	// Retrieve batch results
	resultsRef := status.ResultsURL
	if resultsRef == "" {
		resultsRef = bs.ResultsRef
	}
	log.Info("retrieving batch results", "batch_id", bs.BatchID)
	batchResults, err := client.RetrieveBatch(resultsRef)
	if err != nil {
		return nil, fmt.Errorf("compile: retrieve batch: %w", err)
	}

	// Open DB for indexing
	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("compile: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)

	progress := NewProgress()
	mfPath := filepath.Join(projectDir, ".manifest.json")

	// Build set of known pending sources for CustomID validation
	pendingSet := make(map[string]bool, len(state.Pending))
	for _, p := range state.Pending {
		pendingSet[p] = true
	}

	// Process batch results as summaries
	progress.StartPhase("Processing batch results", len(batchResults))
	var successfulSummaries []SummaryResult

	for _, br := range batchResults {
		// Validate CustomID matches a known pending source
		if !pendingSet[br.CustomID] {
			log.Warn("batch: ignoring unknown custom_id from batch results", "id", br.CustomID)
			continue
		}

		if br.Error != "" {
			result.Errors++
			progress.ItemError(br.CustomID, fmt.Errorf("%s", br.Error))
			state.Failed = append(state.Failed, FailedSource{
				Path:  br.CustomID,
				Error: br.Error,
			})
			continue
		}

		// Track batch usage with batch pricing
		if br.Response != nil && tracker != nil {
			tracker.Track(bs.Pass, br.Response.Model, br.Response.Usage, true)
		}

		// Write summary file
		summaryText := br.Response.Content
		summaryDir := filepath.Join(projectDir, cfg.Output, "summaries")
		os.MkdirAll(summaryDir, 0755)
		summaryPath := filepath.Join(cfg.Output, "summaries", SummaryFilename(br.CustomID))
		absOutputPath := filepath.Join(projectDir, summaryPath)

		frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\nbatch: true\n---\n\n", br.CustomID, timeNow(cfg.Compiler.UserTimeLocation()))
		if err := os.WriteFile(absOutputPath, []byte(frontmatter+summaryText), 0644); err != nil {
			result.Errors++
			progress.ItemError(br.CustomID, err)
			continue
		}

		result.Summarized++
		progress.ItemDone(br.CustomID, summaryPath)

		// Update manifest — ensure source entry exists, then mark compiled
		if _, exists := mf.Sources[br.CustomID]; !exists {
			mf.AddSource(br.CustomID, "", extractType(br.CustomID, cfg.TypeSignals), 0)
		}
		mf.MarkCompiled(br.CustomID, summaryPath, nil)

		// Index
		memStore.Add(memory.Entry{
			ID:          br.CustomID,
			Content:     summaryText,
			Tags:        []string{extractType(br.CustomID, cfg.TypeSignals)},
			ArticlePath: summaryPath,
		})

		if embedder != nil {
			vec, err := embedder.Embed(summaryText)
			if err != nil {
				log.Warn("embedding failed", "source", br.CustomID, "error", err)
			} else {
				vecStore.Upsert(br.CustomID, vec)
			}
		}

		// Track for concept extraction
		successfulSummaries = append(successfulSummaries, SummaryResult{
			SourcePath:  br.CustomID,
			SummaryPath: summaryPath,
			Summary:     summaryText,
		})

		removeFromPending(state, br.CustomID)
		state.Completed = append(state.Completed, br.CustomID)
	}
	progress.EndPhase()

	// Clear batch state
	state.Batch = nil
	state.Pass = 2
	if err := saveCompileState(statePath, state); err != nil {
		log.Warn("failed to save checkpoint after batch retrieval", "error", err)
	}

	// Continue with Pass 2 + 3 synchronously
	if len(successfulSummaries) > 0 {
		model := cfg.Models.Extract
		if model == "" {
			model = cfg.Models.Summarize
			if model == "" {
				model = "gpt-4o-mini"
			}
		}

		client.SetPass("extract")
		extCacheID, _ := client.SetupCache("You are an expert knowledge organizer. Extract structured concepts from source summaries.", model)
		progress.StartPhase("Pass 2: Extract concepts", len(successfulSummaries))
		concepts, err := ExtractConcepts(successfulSummaries, mf.Concepts, client, model)
		if err != nil {
			progress.ItemError("concept extraction", err)
			result.Errors++
		} else {
			result.ConceptsExtracted = len(concepts)
			var conceptNames []string
			for _, c := range concepts {
				conceptNames = append(conceptNames, c.Name)
				mf.AddConcept(c.Name, filepath.Join(cfg.Output, "concepts", c.Name+".md"), c.Sources)
			}
			progress.ConceptsDiscovered(conceptNames)
			progress.EndPhase()
			client.TeardownCache(extCacheID)

			if len(concepts) > 0 {
				writeModel := cfg.Models.Write
				if writeModel == "" {
					writeModel = model
				}
				articleMaxTokens := cfg.Compiler.ArticleMaxTokens
				if articleMaxTokens <= 0 {
					articleMaxTokens = 4000
				}

				merged := ontology.MergedRelations(cfg.Ontology.Relations)
				mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
				ontStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))
				client.SetPass("write")
				writeCacheID, _ := client.SetupCache("You are a knowledge base article writer. Write comprehensive, well-structured wiki articles.", writeModel)
				relPatterns := ontology.RelationPatterns(merged)
				progress.StartPhase("Pass 3: Write articles", len(concepts))
				articles := WriteArticles(ArticleWriteOpts{
					ProjectDir:       projectDir,
					OutputDir:        cfg.Output,
					Client:           client,
					Model:            writeModel,
					MaxTokens:        articleMaxTokens,
					MaxParallel:      cfg.Compiler.MaxParallel,
					MemStore:         memStore,
					VecStore:         vecStore,
					OntStore:         ontStore,
					ChunkStore:       chunkStore,
					DB:               db,
					Embedder:         embedder,
					UserTZ:           cfg.Compiler.UserTimeLocation(),
					ArticleFields:    cfg.Compiler.ArticleFields,
					RelationPatterns: relPatterns,
					ChunkSize:        cfg.Search.ChunkSizeOrDefault(),
					Language:         cfg.Language,
				}, concepts)

				for _, ar := range articles {
					if ar.Error != nil {
						result.Errors++
						progress.ItemError(ar.ConceptName, ar.Error)
					} else {
						result.ArticlesWritten++
						progress.ItemDone(ar.ConceptName, ar.ArticlePath)
					}
				}
				progress.EndPhase()
				client.TeardownCache(writeCacheID)
			}
		}
	}

	// Pass 4: Images (placeholder)
	ExtractImages(projectDir, cfg.Output, nil)

	// Save manifest
	if err := mf.Save(mfPath); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	}

	if err := writeChangelog(projectDir, cfg.Output, result, cfg.Compiler.UserTimeLocation()); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// Clean up checkpoint
	if result.Errors == 0 {
		os.Remove(statePath)
	} else {
		saveCompileState(statePath, state)
	}

	// Git auto-commit
	if cfg.Compiler.AutoCommit {
		commitMsg := fmt.Sprintf("compile (batch): +%d sources, %d concepts, %d articles",
			result.Summarized, result.ConceptsExtracted, result.ArticlesWritten)
		gitpkg.AutoCommit(projectDir, commitMsg)
	}

	progress.Summary(result)

	// Print cost report
	costReport := tracker.Report()
	if costReport.TotalTokens > 0 {
		fmt.Fprint(os.Stderr, llm.FormatReport(costReport))
		result.CostReport = costReport
	}

	return result, nil
}

func loadCompileState(path string) (*CompileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state CompileState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveCompileState(path string, state *CompileState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeFromPending(state *CompileState, path string) {
	for i, p := range state.Pending {
		if p == path {
			state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			return
		}
	}
}

func extractType(path string, typeSignals []config.TypeSignal) string {
	var contentHead string
	if len(typeSignals) > 0 {
		contentHead = extract.ReadHead(path, extract.DefaultHeadRunes)
	}
	return extract.DetectSourceTypeWithSignals(path, contentHead, convertSignals(typeSignals))
}

func convertSignals(typeSignals []config.TypeSignal) []extract.TypeSignal {
	signals := make([]extract.TypeSignal, len(typeSignals))
	for i, s := range typeSignals {
		signals[i] = extract.TypeSignal{
			Type:             s.Type,
			Pattern:          s.Pattern,
			FilenameKeywords: s.FilenameKeywords,
			ContentKeywords:  s.ContentKeywords,
			MinContentHits:   s.MinContentHits,
		}
	}
	return signals
}

// timeNow returns the current time in RFC3339 using the given timezone.
// Used for user-facing timestamps (frontmatter, changelog).
func timeNow(loc *time.Location) string {
	return time.Now().In(loc).Format(time.RFC3339)
}

func filterSuccessful(summaries []SummaryResult) []SummaryResult {
	var result []SummaryResult
	for _, s := range summaries {
		if s.Error == nil && s.Summary != "" {
			result = append(result, s)
		}
	}
	return result
}

// hasSoleSourceOrphan returns true if removing removedPath would orphan at least
// one concept (i.e., a concept whose only source is removedPath).
func hasSoleSourceOrphan(mf *manifest.Manifest, removedPath string) bool {
	for _, name := range mf.ArticlesFromSource(removedPath) {
		if c, ok := mf.Concepts[name]; ok && len(c.Sources) <= 1 {
			return true
		}
	}
	return false
}

// handleRemovedSources processes removed source files, detecting orphaned articles
// and optionally pruning them. When prune=false and an orphan would result, ALL
// state mutations for that source are deferred to preserve recovery via later --prune.
func handleRemovedSources(projectDir string, removed []string, mf *manifest.Manifest,
	memStore *memory.Store, vecStore *vectors.Store, ontStore *ontology.Store, prune bool) {

	for _, removedPath := range removed {
		if !prune && hasSoleSourceOrphan(mf, removedPath) {
			log.Info("deferred source removal (orphaned concepts pending prune)",
				"path", removedPath)
			continue
		}

		affectedConcepts := mf.ArticlesFromSource(removedPath)
		for _, conceptName := range affectedConcepts {
			concept, ok := mf.Concepts[conceptName]
			if !ok {
				continue
			}
			if len(concept.Sources) <= 1 {
				log.Warn("article orphaned (sole source removed)",
					"concept", conceptName,
					"article", concept.ArticlePath,
					"source", removedPath)
				if prune {
					articleAbs := filepath.Join(projectDir, concept.ArticlePath)
					if err := os.Remove(articleAbs); err != nil && !os.IsNotExist(err) {
						log.Warn("failed to delete orphaned article", "path", articleAbs, "error", err)
					} else {
						log.Info("pruned orphaned article", "concept", conceptName, "path", concept.ArticlePath)
					}
					memStore.Delete("concept:" + conceptName)
					vecStore.Delete("concept:" + conceptName)
					ontStore.DeleteEntity(conceptName)
					delete(mf.Concepts, conceptName)
				}
			} else {
				var updated []string
				for _, s := range concept.Sources {
					if s != removedPath {
						updated = append(updated, s)
					}
				}
				concept.Sources = updated
				mf.Concepts[conceptName] = concept
				log.Info("updated concept sources (removed source)",
					"concept", conceptName, "remaining_sources", len(updated))
			}
		}

		mf.RemoveSource(removedPath)
		memStore.Delete(removedPath)
		vecStore.Delete(removedPath)
		log.Info("removed source", "path", removedPath)
	}
}

func writeChangelog(projectDir string, outputDir string, result *CompileResult, loc *time.Location) error {
	changelogPath := filepath.Join(projectDir, outputDir, "CHANGELOG.md")

	entry := fmt.Sprintf("## %s\n\n- Added: %d sources\n- Modified: %d sources\n- Removed: %d sources\n- Summarized: %d\n- Concepts extracted: %d\n- Articles written: %d\n- Errors: %d\n\n",
		timeNow(loc), result.Added, result.Modified, result.Removed,
		result.Summarized, result.ConceptsExtracted, result.ArticlesWritten, result.Errors)

	// Prepend to existing changelog
	existing, _ := os.ReadFile(changelogPath)
	header := "# CHANGELOG\n\nCompilation history for sage-wiki.\n\n"
	if len(existing) > 0 {
		content := string(existing)
		if idx := strings.Index(content, "\n## "); idx >= 0 {
			content = content[idx+1:]
		}
		return os.WriteFile(changelogPath, []byte(header+entry+content), 0644)
	}
	return os.WriteFile(changelogPath, []byte(header+entry), 0644)
}
