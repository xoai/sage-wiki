package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/fsutil"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// CompileOpts configures a compilation run.
type CompileOpts struct {
	// Ctx carries cancellation (Ctrl-C / MCP deadline) into the LLM calls and
	// compile passes; nil is treated as context.Background().
	Ctx     context.Context
	DryRun  bool
	Fresh   bool             // ignore checkpoint
	Batch   bool             // use batch API (async, 50% discount)
	NoCache bool             // disable prompt caching
	Prune   bool             // delete orphaned articles when sources removed
	Tracker *llm.CostTracker // optional cost tracker
}

// CompileResult summarizes what happened during compilation.
type CompileResult struct {
	Added             int
	Modified          int
	Removed           int
	Summarized        int
	ConceptsExtracted int
	ArticlesWritten   int
	Errors            int
	EmbedErrors       int
	CostReport        *llm.CostReport // nil if no LLM calls were made
	TierIndexed       int             // sources indexed at Tier 0
	TierEmbedded      int             // sources embedded at Tier 1
	TierCompiled      int             // sources sent through full pipeline (Tier 3)
}

// CompileState tracks progress for checkpoint/resume (ADR-018).
type CompileState struct {
	CompileID string         `json:"compile_id"`
	StartedAt string         `json:"started_at"`
	Pass      int            `json:"pass"`
	Completed []string       `json:"completed"`
	Pending   []string       `json:"pending"`
	Failed    []FailedSource `json:"failed,omitempty"`
	Batch     *BatchState    `json:"batch,omitempty"` // non-nil when batch is in flight
}

// BatchState tracks an in-flight batch job for checkpoint/resume.
type BatchState struct {
	BatchID     string `json:"batch_id"`
	Provider    string `json:"provider"`
	Pass        string `json:"pass"`        // which compiler pass (summarize, extract)
	ResultsRef  string `json:"results_ref"` // Anthropic: results URL; OpenAI: output_file_id
	SubmittedAt string `json:"submitted_at"`

	// PathByID maps a wire-level custom_id (a short hash of the source path)
	// back to the original source path. The wire ID is required to stay short
	// because some providers (Zhipu GLM) cap custom_id at 64 chars while OpenAI
	// and Anthropic allow long IDs. Populated by submitBatch, consumed by
	// resumeBatch. Empty for legacy checkpoints written before this fix —
	// resumeBatch then falls back to treating custom_id as the literal path.
	// Issue #89.
	PathByID map[string]string `json:"path_by_id,omitempty"`
}

// batchIDForPath produces a short stable custom_id for a source path that
// fits within every provider's custom_id length limit (Zhipu GLM caps it at
// 64 chars; OpenAI and Anthropic are more permissive). 16 hex chars (64 bits
// of SHA-256) gives birthday-paradox-safe uniqueness up to ~4B entries.
func batchIDForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

type FailedSource struct {
	Path     string `json:"path"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

// orBackground returns ctx, or context.Background() when ctx is nil, so the
// manifest lock's context-aware wait never dereferences a nil context. The
// manifest MergeSave runs on the completed-run branch where cancellation should
// no longer drop the result, but the lock still honors an explicit cancel.
func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
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

	// Snapshot the manifest at Load as the merge base (D3). At Save time the
	// compile reloads fresh under the lock and applies its delta (ours-base) so a
	// short writer (MCP/CLI/ingest) that landed mid-compile is preserved rather
	// than clobbered. Taken before any mutation, so ours-base is exactly this
	// run's mutations.
	base := mf.Clone()

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
	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("compile: create LLM client: %w", err)
	}

	// Attach cost tracker (with optional price override from config)
	tracker := opts.Tracker
	if tracker == nil {
		tracker = llm.NewCostTracker(cfg.API.Provider, cfg.Compiler.TokenPriceOverride)
	}
	client.SetTracker(tracker)

	// Check for pending batch to resume (P1-3: batch state lives in
	// .sage/batch-state.json; a legacy compile-state.json in-flight batch is
	// split into it here, on the first run of the new binary). Skipped under
	// --fresh, matching the legacy state load below; T4 upgrades this to
	// skip-AND-clear.
	if !opts.Fresh {
		bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
		if err != nil {
			return nil, fmt.Errorf("compile: load batch checkpoint: %w", err)
		}
		if bcp != nil && bcp.Batch != nil {
			if client.ProviderName() != bcp.Batch.Provider {
				return nil, fmt.Errorf("compile: provider changed from %s to %s since batch was submitted — clear checkpoint with --fresh or switch back", bcp.Batch.Provider, client.ProviderName())
			}
			return resumeBatch(projectDir, client, cfg, mf, base, bcp, tracker, opts)
		}
	}

	// Subscription auth: disable batch mode (subscription tokens lack batch API access)
	if cfg.API.Auth == "subscription" && (opts.Batch || cfg.Compiler.Mode == "batch" || cfg.Compiler.Mode == "auto") {
		log.Info("batch mode unavailable with subscription auth, using standard mode")
		fmt.Fprintln(os.Stderr, "Batch mode unavailable with subscription auth, using standard mode.")
		opts.Batch = false
		if cfg.Compiler.Mode == "batch" || cfg.Compiler.Mode == "auto" {
			cfg.Compiler.Mode = "standard"
		}
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
		return submitBatch(projectDir, client, cfg, mf, diff, tracker)
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

	// Load external parsers if enabled and trusted
	var exOpts []extract.ExtractOpts
	if cfg.Parsers.External {
		if !cfg.Parsers.TrustExternal {
			fmt.Fprintln(os.Stderr, "Warning: parsers.external is true but parsers.trust_external is false.")
			fmt.Fprintln(os.Stderr, "External parsers execute arbitrary code WITHOUT a sandbox on most systems.")
			fmt.Fprintln(os.Stderr, "Set parsers.trust_external: true in config.yaml to acknowledge this risk.")
		} else {
			exReg, err := extract.LoadExternalParsers(projectDir)
			if err != nil {
				log.Warn("loading external parsers", "error", err)
			} else if exReg.HasParsers() {
				exReg.Trusted = true
				fmt.Fprintln(os.Stderr, "External parsers enabled (trusted mode). Running parser code from this project.")
				exOpts = []extract.ExtractOpts{{ExternalParsers: exReg, ParsersEnabled: true}}
			}
		}
	}

	// Tier 0: FTS5 index only (no LLM, ~5ms/doc)
	tier0Pending, _ := itemStore.ListPending(0)
	if len(tier0Pending) > 0 {
		progress.StartPhase("Tier 0: Index sources", len(tier0Pending))
		indexed := indexRawSources(projectDir, tier0Pending, memStore, itemStore, exOpts...)
		result.TierIndexed = indexed
		log.Info("tier 0 indexing complete", "indexed", indexed)
		progress.EndPhase()
	}

	// Tier 1: FTS5 + vector embed (~200ms/doc)
	tier1Pending, _ := itemStore.ListPending(1)
	if len(tier1Pending) > 0 {
		progress.StartPhase("Tier 1: Index + embed sources", len(tier1Pending))
		indexed, embedded := indexAndEmbedSources(projectDir, tier1Pending, memStore, vecStore, embedder, itemStore, bp, chunkStore, cfg.Search.ChunkSizeOrDefault(), db, exOpts...)
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

	// pipelineIncomplete is set when a tiered pipeline run does not complete Pass
	// 2/3 (cancelled or a total-extraction failure). Such a run persists NO new
	// compile state: the compile_items MarkPass advances are skipped below and the
	// manifest Save at the end of Compile is skipped, discarding this run's
	// in-memory manifest mutations (AddSource / MarkCompiled / AddConcept). The next
	// compile's Diff then re-includes these sources and reprocesses them cleanly.
	pipelineIncomplete := false
	if len(toProcess) > 0 {
		cacheEnabled := cfg.Compiler.PromptCacheEnabled() && !opts.NoCache
		if cacheEnabled && cfg.API.Auth == "subscription" && cfg.API.Provider == "gemini" {
			cacheEnabled = false
			log.Info("prompt caching unavailable with Gemini subscription auth")
			fmt.Fprintln(os.Stderr, "Prompt caching unavailable with Gemini subscription auth.")
		}
		pipelineResult := runFullPipeline(toProcess, FullPipelineOpts{
			Ctx:          opts.Ctx,
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

		// When Pass 2/3 did not complete (cancel or total-extraction failure), this
		// run is incomplete: mark nothing and (below) skip the manifest Save, so no
		// new compile state is persisted. Advancing even the "summarized" flag or
		// saving the manifest would record half-done work — the earlier surgical
		// rollback (RemoveSource on just the sources) left the run's concepts
		// orphaned, since RemoveSource deletes Sources only. P1-1 / C1.
		pipelineIncomplete = !pipelineResult.Pass23Completed
		if !pipelineIncomplete {
			// Pass 2/3 completed — advance all three pass flags for the sources that
			// summarized successfully so ListPending treats them as fully compiled.
			succeeded := make(map[string]bool)
			for _, p := range pipelineResult.SucceededSources {
				succeeded[p] = true
			}
			for _, s := range toProcess {
				if succeeded[s.Path] {
					for _, pass := range []string{"summarized", "extracted", "written"} {
						if err := itemStore.MarkPass(s.Path, pass); err != nil {
							log.Warn("mark pass failed", "path", s.Path, "pass", pass, "error", err)
						}
					}
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

	// Post-compile sweep: strip [[wikilinks]] pointing at concepts that don't
	// exist on disk after this compile finished. Pass 3's writer prompts the
	// LLM to use generous cross-references, but Pass 2 extracts concepts
	// conservatively, so a wiki of a non-ML corpus can end up with the
	// majority of links being phantom. Issue #90.
	MaybeStripBrokenWikilinks(projectDir, cfg.Output, cfg.Compiler.StripBrokenLinksEnabled(), memStore)

	// Save manifest — unless a tiered pipeline run was interrupted before Pass 2/3
	// completed. Saving then would persist this run's half-done manifest mutations
	// (AddSource/MarkCompiled/AddConcept); skipping the Save discards them so the
	// next compile reprocesses the sources from a clean state (pre-P1-1 hard-kill
	// behavior). Any handleRemovedSources manifest edits are discarded too, but they
	// re-run next compile since the removed sources are still absent from the corpus.
	// P1-1 / C1.
	if pipelineIncomplete {
		log.Info("compile interrupted before Pass 2/3 completed — manifest not saved; sources will reprocess on next compile")
	} else if err := manifest.MergeSave(orBackground(opts.Ctx), mfPath, base, mf); err != nil {
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

	// Check for source changes that invalidate confirmed outputs
	if cfg.Trust.IncludeOutputsMode() == "verified" {
		trustStore := trust.NewStore(db)
		stores := trust.IndexStores{
			MemStore: memStore, VecStore: vecStore, OntStore: pipelineOntStore,
			ChunkStore: chunkStore, DB: db,
		}
		demoted, err := trust.CheckSourceChanges(trustStore, projectDir, &stores)
		if err != nil {
			log.Warn("trust source check failed", "error", err)
		} else if demoted > 0 {
			log.Info("demoted stale outputs", "count", demoted)
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
// Saves the batch checkpoint (.sage/batch-state.json) and exits — the next
// `compile` resumes via resumeBatch.
func submitBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	diff *DiffResult,
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

	// Load external parsers for batch extraction
	var batchExOpts []extract.ExtractOpts
	if cfg.Parsers.External && cfg.Parsers.TrustExternal {
		exReg, err := extract.LoadExternalParsers(projectDir)
		if err == nil && exReg.HasParsers() {
			exReg.Trusted = true
			batchExOpts = []extract.ExtractOpts{{ExternalParsers: exReg, ParsersEnabled: true}}
		}
	}

	// Build batch requests — extract content and render prompts.
	// pathByID maps each wire-level custom_id (a hash of the path) back to
	// the source path; persisted in BatchState for resumeBatch (issue #89).
	var requests []llm.BatchRequest
	pathByID := make(map[string]string)
	for _, src := range toProcess {
		absPath := filepath.Join(projectDir, src.Path)
		content, err := extract.Extract(absPath, src.Type, batchExOpts...)
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

		customID := batchIDForPath(src.Path)
		pathByID[customID] = src.Path
		requests = append(requests, llm.BatchRequest{
			CustomID: customID,
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

	// Pending list holds source PATHS (used by resumeBatch's result
	// validation) — the wire-level IDs are kept in BatchState.PathByID.
	pending := make([]string, 0, len(pathByID))
	for _, path := range pathByID {
		pending = append(pending, path)
	}

	// Save the batch checkpoint — the ONLY checkpoint written on this path
	// (P1-3; no compile-state.json anywhere).
	utcNow := time.Now().UTC().Format(time.RFC3339)
	bcp := &BatchCheckpoint{
		CompileID: time.Now().Format("20060102-150405"),
		StartedAt: utcNow,
		Batch: &BatchState{
			BatchID:     batchID,
			Provider:    client.ProviderName(),
			Pass:        "summarize",
			SubmittedAt: utcNow,
			PathByID:    pathByID,
		},
		Pending: pending,
	}
	if err := saveBatchCheckpoint(projectDir, bcp); err != nil {
		return nil, fmt.Errorf("compile: CRITICAL — batch %s submitted but checkpoint save failed: %w (batch ID may be lost)", batchID, err)
	}

	fmt.Fprintf(os.Stderr, "\n📦 Batch submitted: %s\n", batchID)
	fmt.Fprintf(os.Stderr, "   Provider: %s\n", client.ProviderName())
	fmt.Fprintf(os.Stderr, "   Sources:  %d\n", len(requests))
	fmt.Fprintf(os.Stderr, "   Run `sage-wiki compile` again to check status and retrieve results.\n\n")

	return result, nil
}

// resumeBatch polls and retrieves a previously submitted batch, then continues the pipeline.
// The batch checkpoint (bcp) is the only resume state (P1-3); every terminal
// path retires it via retireBatchCheckpoint (strip-then-conditional-delete).
func resumeBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	base *manifest.Manifest,
	bcp *BatchCheckpoint,
	tracker *llm.CostTracker,
	opts CompileOpts,
) (*CompileResult, error) {
	result := &CompileResult{}
	bs := bcp.Batch

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
		retireBatchCheckpoint(projectDir)
		return result, nil

	case llm.BatchFailed:
		log.Error("batch failed", "batch_id", bs.BatchID)
		fmt.Fprintf(os.Stderr, "✗ Batch %s failed. Re-run with `compile --batch` to resubmit.\n", bs.BatchID)
		retireBatchCheckpoint(projectDir)
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
	pendingSet := make(map[string]bool, len(bcp.Pending))
	for _, p := range bcp.Pending {
		pendingSet[p] = true
	}

	// Summary naming (issue #107): resolve roots once and warn on collisions
	// over the full pending set, matching the standard/on-demand path.
	batchRoots := sourceRootPaths(cfg.Sources)
	warnSummaryNameCollisions(bcp.Pending, batchRoots, cfg.Compiler.SummaryNamingOrDefault())

	// Process batch results as summaries
	progress.StartPhase("Processing batch results", len(batchResults))
	var successfulSummaries []SummaryResult

	for _, br := range batchResults {
		// Translate the wire-level custom_id back to the source path. New
		// batches (post-fix) populate bs.PathByID; legacy checkpoints written
		// before this fix have an empty map, in which case the custom_id IS
		// the path and we use it directly. Issue #89.
		path := br.CustomID
		if bs.PathByID != nil {
			if mapped, ok := bs.PathByID[br.CustomID]; ok {
				path = mapped
			} else {
				log.Warn("batch: unknown custom_id (no path mapping)", "id", br.CustomID)
				continue
			}
		}

		// Validate path matches a known pending source
		if !pendingSet[path] {
			log.Warn("batch: ignoring unknown source from batch results", "id", br.CustomID, "path", path)
			continue
		}

		if br.Error != "" {
			result.Errors++
			progress.ItemError(path, fmt.Errorf("%s", br.Error))
			continue
		}

		// Track batch usage with batch pricing
		if br.Response != nil && tracker != nil {
			tracker.Track(bs.Pass, br.Response.Model, br.Response.Usage, true)
		}

		// Guard: an empty batch summary must fail the source (no validateSummary
		// backstop on this path) rather than write a hollow summary file.
		if gErr := emptyContentError(br.Response, "batch summary", path); gErr != nil {
			result.Errors++
			progress.ItemError(path, gErr)
			continue
		}

		// Write summary file (issue #107: honor the configured naming scheme via
		// the same helper as the standard path, so batch/standard never diverge).
		summaryText := br.Response.Content
		summaryDir := filepath.Join(projectDir, cfg.Output, "summaries")
		os.MkdirAll(summaryDir, 0755)
		summaryName := SummaryFilenameMode(path, resolveSourceRoot(path, batchRoots), cfg.Compiler.SummaryNamingOrDefault())
		summaryPath := filepath.Join(cfg.Output, "summaries", summaryName)
		absOutputPath := filepath.Join(projectDir, summaryPath)

		frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\nbatch: true\n---\n\n", path, timeNow(cfg.Compiler.UserTimeLocation()))
		// Canonical write-then-index order (I2): summary written atomically first.
		if err := fsutil.WriteFileAtomic(absOutputPath, []byte(frontmatter+summaryText), 0644); err != nil {
			result.Errors++
			progress.ItemError(path, err)
			continue
		}

		result.Summarized++
		progress.ItemDone(path, summaryPath)

		// Update manifest — ensure source entry exists with current resolved type,
		// then mark compiled. Refreshing Type on existing entries propagates
		// config-driven type changes on recompile.
		resolvedType := TypeForFile(projectDir, path, cfg)
		if _, exists := mf.Sources[path]; !exists {
			mf.AddSource(path, "", resolvedType, 0)
		} else {
			src := mf.Sources[path]
			src.Type = resolvedType
			mf.Sources[path] = src
		}
		mf.MarkCompiled(path, summaryPath, nil)

		// Index
		memStore.Add(memory.Entry{
			ID:          path,
			Content:     summaryText,
			Tags:        []string{resolvedType},
			ArticlePath: summaryPath,
		})

		if embedder != nil {
			vec, err := embedder.Embed(summaryText)
			if err != nil {
				log.Warn("embedding failed", "source", path, "error", err)
			} else {
				vecStore.Upsert(path, vec)
			}
		}

		// Track for concept extraction
		successfulSummaries = append(successfulSummaries, SummaryResult{
			SourcePath:  path,
			SummaryPath: summaryPath,
			Summary:     summaryText,
		})
	}
	progress.EndPhase()

	// The batch is consumed: summaries are on disk (atomic writes above), and
	// the remaining Pass 2/3 work is re-derivable — the manifest is only saved
	// at the end of this run, so a crash re-diffs these sources and the
	// standard path reprocesses them (batch summaries lack source_hash
	// frontmatter, so they re-generate: recompute, never loss). Retire the
	// checkpoint now (strip legacy marker first; delete only on strip
	// success — spec D5).
	retireBatchCheckpoint(projectDir)

	// Continue with Pass 2 + 3 synchronously
	batchPass23OK := true // set false if extraction fails or the run is cancelled
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
		concepts, err := ExtractConcepts(opts.Ctx, successfulSummaries, mf.Concepts, client, model, cfg.Compiler.ExtractBatchSize, cfg.Compiler.ExtractMaxTokens, cfg.Compiler.MaxParallel)
		if err != nil {
			progress.ItemError("concept extraction", err)
			result.Errors++
			batchPass23OK = false
			progress.EndPhase()
			client.TeardownCache(extCacheID)
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
					Ctx:                opts.Ctx,
					ProjectDir:         projectDir,
					OutputDir:          cfg.Output,
					Client:             client,
					Model:              writeModel,
					MaxTokens:          articleMaxTokens,
					MaxParallel:        cfg.Compiler.MaxParallel,
					MemStore:           memStore,
					VecStore:           vecStore,
					OntStore:           ontStore,
					ChunkStore:         chunkStore,
					DB:                 db,
					Embedder:           embedder,
					UserTZ:             cfg.Compiler.UserTimeLocation(),
					ArticleFields:      cfg.Compiler.ArticleFields,
					RelationPatterns:   relPatterns,
					ChunkSize:          cfg.Search.ChunkSizeOrDefault(),
					Language:           cfg.Language,
					AntiPatternPhrases: cfg.Compiler.AntiPatternPhrasesOrDefault(),
					AllConcepts:        manifestConceptRefs(mf.Concepts),
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

	// If Pass 2/3 did not complete (extraction failure or cancel), this batch-resume
	// run is incomplete: skip the manifest Save entirely (below), discarding the
	// run's in-memory manifest mutations (AddSource/MarkCompiled/AddConcept) so the
	// next compile's Diff re-includes these sources and reprocesses them. The earlier
	// surgical RemoveSource dropped only the sources, leaving the run's concepts
	// orphaned (RemoveSource deletes Sources only). Mirrors the full/on-demand paths.
	// P1-1 / C1.
	if opts.Ctx != nil && opts.Ctx.Err() != nil {
		batchPass23OK = false
	}

	// Save manifest — unless the batch run was interrupted before Pass 2/3 completed.
	if !batchPass23OK {
		log.Info("batch compile interrupted before Pass 2/3 completed — manifest not saved; sources will reprocess on next compile")
	} else if err := manifest.MergeSave(orBackground(opts.Ctx), mfPath, base, mf); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	}

	if err := writeChangelog(projectDir, cfg.Output, result, cfg.Compiler.UserTimeLocation()); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// Check for source changes that invalidate confirmed outputs
	if cfg.Trust.IncludeOutputsMode() == "verified" {
		trustStore := trust.NewStore(db)
		batchMerged := ontology.MergedRelations(cfg.Ontology.Relations)
		batchMergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		stores := trust.IndexStores{
			MemStore: memStore, VecStore: vecStore,
			OntStore:   ontology.NewStore(db, ontology.ValidRelationNames(batchMerged), ontology.ValidEntityTypeNames(batchMergedTypes)),
			ChunkStore: chunkStore, DB: db,
		}
		demoted, err := trust.CheckSourceChanges(trustStore, projectDir, &stores)
		if err != nil {
			log.Warn("trust source check failed", "error", err)
		} else if demoted > 0 {
			log.Info("demoted stale outputs", "count", demoted)
		}
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
