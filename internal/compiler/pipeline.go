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
	CostReport         *llm.CostReport // nil if no LLM calls were made
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
	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit)
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

	// Initialize checkpoint state
	if state == nil {
		state = &CompileState{
			CompileID: time.Now().Format("20060102-150405"),
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Pass:      1,
		}
	}

	// Merge new files from diff into checkpoint pending list
	// This handles files added while the watcher was stopped
	completedSet := make(map[string]bool)
	for _, p := range state.Completed {
		completedSet[p] = true
	}
	pendingSet := make(map[string]bool)
	for _, p := range state.Pending {
		pendingSet[p] = true
	}
	for _, s := range append(diff.Added, diff.Modified...) {
		if !completedSet[s.Path] && !pendingSet[s.Path] {
			state.Pending = append(state.Pending, s.Path)
			pendingSet[s.Path] = true
			log.Info("new source added to compile queue", "path", s.Path)
		}
	}

	// If new files were added, reset pass to 1 so they get summarized
	newFiles := false
	var toProcess []SourceInfo
	for _, s := range append(diff.Added, diff.Modified...) {
		if pendingSet[s.Path] {
			toProcess = append(toProcess, s)
			if !completedSet[s.Path] {
				newFiles = true
			}
		}
	}
	if newFiles && state.Pass > 1 {
		log.Info("new sources detected, resetting to Pass 1")
		state.Pass = 1
	}
	client.SetPass("summarize")
	// Setup prompt cache for summarize pass (respects --no-cache and config)
	cacheEnabled := cfg.Compiler.PromptCacheEnabled() && !opts.NoCache
	var sumCacheID string
	if cacheEnabled {
		sumCacheID, _ = client.SetupCache("You are a research assistant creating structured summaries for a personal knowledge wiki.", cfg.Models.Summarize)
	}
	progress.StartPhase("Pass 1: Summarize sources", len(toProcess))

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxTokens := cfg.Compiler.SummaryMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	summaries := Summarize(projectDir, cfg.Output, toProcess, client, model, maxTokens, cfg.Compiler.MaxParallel, cfg.Compiler.UserTimeLocation(), cfg.Language)

	for _, sr := range summaries {
		if sr.Error != nil {
			result.Errors++
			progress.ItemError(sr.SourcePath, sr.Error)
			state.Failed = append(state.Failed, FailedSource{
				Path:  sr.SourcePath,
				Error: sr.Error.Error(),
			})
			continue
		}

		result.Summarized++
		progress.ItemDone(sr.SourcePath, sr.SummaryPath)

		// Update manifest — register or update source hash
		for _, s := range toProcess {
			if s.Path == sr.SourcePath {
				if _, exists := mf.Sources[sr.SourcePath]; !exists {
					// New source
					mf.AddSource(s.Path, s.Hash, s.Type, s.Size)
				} else {
					// Existing source — update hash so it's not flagged as modified next time
					src := mf.Sources[sr.SourcePath]
					src.Hash = s.Hash
					mf.Sources[sr.SourcePath] = src
				}
				break
			}
		}
		mf.MarkCompiled(sr.SourcePath, sr.SummaryPath, sr.Concepts)

		// Index in FTS5
		memStore.Add(memory.Entry{
			ID:          sr.SourcePath,
			Content:     sr.Summary,
			Tags:        []string{extractType(sr.SourcePath)},
			ArticlePath: sr.SummaryPath,
		})

		// Generate embedding
		if embedder != nil {
			vec, err := embedder.Embed(sr.Summary)
			if err != nil {
				log.Warn("embedding failed", "source", sr.SourcePath, "error", err)
			} else {
				vecStore.Upsert(sr.SourcePath, vec)
			}
		}

		// Update checkpoint
		removeFromPending(state, sr.SourcePath)
		state.Completed = append(state.Completed, sr.SourcePath)
		saveCompileState(statePath, state)
	}

	// Update checkpoint pass
	client.TeardownCache(sumCacheID)
	state.Pass = 2
	saveCompileState(statePath, state)

	// Pass 2: Concept extraction
	successfulSummaries := filterSuccessful(summaries)
	if len(successfulSummaries) > 0 {
		extractModel := cfg.Models.Extract
		if extractModel == "" {
			extractModel = model
		}

		client.SetPass("extract")
		var extCacheID string
		if cacheEnabled {
			extCacheID, _ = client.SetupCache("You are an expert knowledge organizer. Extract structured concepts from source summaries.", extractModel)
		}
		progress.StartPhase("Pass 2: Extract concepts", len(successfulSummaries))
		concepts, err := ExtractConcepts(successfulSummaries, mf.Concepts, client, extractModel)
		if err != nil {
			progress.ItemError("concept extraction", err)
			result.Errors++
		} else {
			result.ConceptsExtracted = len(concepts)

			// Report discovered concepts
			var conceptNames []string
			for _, c := range concepts {
				conceptNames = append(conceptNames, c.Name)
				mf.AddConcept(c.Name, filepath.Join(cfg.Output, "concepts", c.Name+".md"), c.Sources)
			}
			progress.ConceptsDiscovered(conceptNames)
			progress.EndPhase()
			client.TeardownCache(extCacheID)

			// Pass 3: Write articles
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
				var writeCacheID string
				if cacheEnabled {
					writeCacheID, _ = client.SetupCache("You are a knowledge base article writer. Write comprehensive, well-structured wiki articles.", writeModel)
				}
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
		baseName := strings.TrimSuffix(filepath.Base(br.CustomID), filepath.Ext(br.CustomID))
		summaryDir := filepath.Join(projectDir, cfg.Output, "summaries")
		os.MkdirAll(summaryDir, 0755)
		summaryPath := filepath.Join(cfg.Output, "summaries", baseName+".md")
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
			mf.AddSource(br.CustomID, "", extractType(br.CustomID), 0)
		}
		mf.MarkCompiled(br.CustomID, summaryPath, nil)

		// Index
		memStore.Add(memory.Entry{
			ID:          br.CustomID,
			Content:     summaryText,
			Tags:        []string{extractType(br.CustomID)},
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

func extractType(path string) string {
	return extract.DetectSourceType(path)
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

// handleRemovedSources processes removed source files, detecting orphaned articles
// and optionally pruning them. Must be called BEFORE mf.RemoveSource() since it
// needs the manifest entries to look up affected concepts.
func handleRemovedSources(projectDir string, removed []string, mf *manifest.Manifest,
	memStore *memory.Store, vecStore *vectors.Store, ontStore *ontology.Store, prune bool) {

	for _, removedPath := range removed {
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
