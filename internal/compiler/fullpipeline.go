package compiler

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/shopspring/decimal"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"

	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
)

// sourceRootPaths extracts the configured source root paths (sources[].path),
// used to strip the root prefix under relative summary naming (issue #107).
func sourceRootPaths(sources []config.Source) []string {
	roots := make([]string, 0, len(sources))
	for _, s := range sources {
		roots = append(roots, s.Path)
	}
	return roots
}

// sourceInfoPaths extracts the discovered file paths from a SourceInfo slice.
func sourceInfoPaths(sources []SourceInfo) []string {
	paths := make([]string, 0, len(sources))
	for _, s := range sources {
		paths = append(paths, s.Path)
	}
	return paths
}

// warnSummaryNameCollisions logs a warning when, under the given naming mode,
// two distinct source paths map to the same summary filename (issue #107).
// Best-effort: it needs the whole source set, so it runs at set-level sites
// (the full/on-demand pipeline and batch submission), not per file. Under the
// default "full" mode collisions cannot occur, so this is a no-op there.
func warnSummaryNameCollisions(paths, roots []string, mode string) {
	if mode != "relative" {
		return
	}
	seen := make(map[string]string, len(paths))
	for _, p := range paths {
		name := SummaryFilenameMode(p, resolveSourceRoot(p, roots), mode)
		if prev, ok := seen[name]; ok && prev != p {
			log.Warn("summary_naming: relative filename collision — one summary overwrites another",
				"filename", name, "source_a", prev, "source_b", p)
			continue
		}
		seen[name] = p
	}
}

// FullPipelineOpts bundles all parameters needed for the full compilation
// pipeline (Pass 1 → Pass 2 → Pass 3).
type FullPipelineOpts struct {
	Ctx        context.Context // cancellation context; nil = background
	ProjectDir string
	Config     *config.Config
	Client     *llm.Client
	Manifest   *manifest.Manifest
	DB         store.DBHandle
	MemStore   store.EntryStore
	VecStore   store.VectorStore
	ChunkStore store.ChunkStore
	OntStore   store.OntologyStore
	TrustStore store.TrustStore // optional — edge conflicts skipped when nil (P3-6)
	Embedder   embed.Embedder

	// Prompts is the per-workspace template registry (SPEC-01); nil = the
	// prompts package default (CLI behavior).
	Prompts *prompts.Registry

	// MaxCost + Tracker implement the budget guard (pkg/engine
	// CompileRequest.MaxCost): between passes, if accumulated cost exceeds
	// MaxCost the run stops with BudgetExhausted set. nil = no guard.
	MaxCost      *decimal.Decimal
	Tracker      *llm.CostTracker
	Backpressure *BackpressureController
	ItemStore    store.CompileItemStore // optional — for per-article quality scoring
	CacheEnabled bool

	// Sink, when set, receives the pipeline-interior events (SPEC-07):
	// entity_resolved from the resolution pass (edge events attach at the
	// ontology store, Task 6).
	Sink     events.Sink
	Progress *Progress

	// Budgets, when set, enforces the per-doc compile_doc_timeout
	// stopwatch over the doc's Pass-1 and Pass-2b units (SPEC-08 D6).
	Budgets *DocBudgets
	// JobID labels the compile_doc_finished emissions for timeout docs.
	JobID string
}

// FullPipelineResult summarizes what the full pipeline produced.
type FullPipelineResult struct {
	Summarized        int
	ConceptsExtracted int
	ArticlesWritten   int
	Errors            int
	EmbedErrors       int
	SucceededSources  []string // source paths that completed summarization successfully
	// Pass23Completed is true only when Pass 2 (extract) and Pass 3 (write) ran to
	// the end without a total-extraction failure and without cancellation. Callers
	// must not mark SucceededSources extracted/written unless this is true, or an
	// interrupted/failed run leaves them un-resumable. P1-1 / C1.
	Pass23Completed bool
	// BudgetExhausted is true when the run stopped early at MaxCost.
	BudgetExhausted bool
}

// runFullPipeline executes Pass 1 (summarize) → Pass 2 (extract) → Pass 3 (write)
// on the given sources. This is the existing LLM compilation pipeline, extracted
// from Compile() for reuse by both the tiered orchestrator and compile-on-demand.
// budgetExceeded reports whether accumulated tracked cost passed MaxCost.
// Unknown cost (nil) never trips the guard — an unknown spend cannot be
// compared to a budget (SPEC-05: unknown is not zero).
func budgetExceeded(tracker *llm.CostTracker, max *decimal.Decimal) bool {
	if max == nil || tracker == nil {
		return false
	}
	rep := tracker.Report()
	return rep.Cost != nil && rep.Cost.GreaterThan(*max)
}

func runFullPipeline(sources []SourceInfo, opts FullPipelineOpts) *FullPipelineResult {
	result := &FullPipelineResult{}
	cfg := opts.Config
	client := opts.Client
	mf := opts.Manifest
	progress := opts.Progress
	if progress == nil {
		progress = NewProgress()
	}

	// Pass 1: Summarize
	client.SetPass("summarize")
	var sumCacheID string
	if opts.CacheEnabled {
		sumCacheID, _ = client.SetupCache("You are a research assistant creating structured summaries for a personal knowledge wiki.", cfg.Models.Summarize)
	}
	progress.StartPhase("Pass 1: Summarize sources", len(sources))

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxTokens := cfg.Compiler.SummaryMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	// Load external parsers for summarization
	var sumExOpts []extract.ExtractOpts
	if cfg.Parsers.External && cfg.Parsers.TrustExternal {
		exReg, err := extract.LoadExternalParsers(opts.ProjectDir)
		if err != nil {
			log.Warn("loading external parsers for summarize", "error", err)
		} else if exReg.HasParsers() {
			exReg.Trusted = true
			sumExOpts = []extract.ExtractOpts{{ExternalParsers: exReg, ParsersEnabled: true}}
		}
	}

	summaryNaming := cfg.Compiler.SummaryNamingOrDefault()
	sourceRoots := sourceRootPaths(cfg.Sources)
	warnSummaryNameCollisions(sourceInfoPaths(sources), sourceRoots, summaryNaming)

	summaries := Summarize(SummarizeOpts{
		Temperature:   cfg.Compiler.CompileTemperature(),
		Prompts:       opts.Prompts,
		Ctx:           opts.Ctx,
		ProjectDir:    opts.ProjectDir,
		OutputDir:     cfg.Output,
		Sources:       sources,
		Client:        client,
		Model:         model,
		MaxTokens:     maxTokens,
		MaxParallel:   cfg.Compiler.MaxParallel,
		UserTZ:        cfg.Compiler.UserTimeLocation(),
		Language:      cfg.Language,
		Backpressure:  opts.Backpressure,
		ExtractOpts:   sumExOpts,
		SummaryNaming: summaryNaming,
		SourceRoots:   sourceRoots,
		Budgets:       opts.Budgets,
	})

	for _, sr := range summaries {
		// A cancelled source (compile interrupted, or its LLM call cancelled mid-
		// flight) is neither success nor failure. Skip it: don't count an error,
		// don't record a failure, and don't mark it compiled — so the next
		// compile reprocesses it cleanly.
		if sr.Cancelled || errors.Is(sr.Error, context.Canceled) || errors.Is(sr.Error, context.DeadlineExceeded) {
			continue
		}
		// SPEC-08 AC11: a per-doc budget expiry is a typed timeout — the doc
		// is left unprocessed (retryable next compile) and both events fire:
		// compile_doc_finished{Skipped:true} + limit_exceeded.
		if errors.Is(sr.Error, limits.ErrTimeout) {
			emitDocTimeout(opts.Sink, opts.ProjectDir, opts.JobID, sr.SourcePath, sr.Error)
			progress.ItemError(sr.SourcePath, sr.Error)
			continue
		}
		if sr.Error != nil {
			result.Errors++
			progress.ItemError(sr.SourcePath, sr.Error)
			continue
		}

		result.Summarized++
		result.SucceededSources = append(result.SucceededSources, sr.SourcePath)
		progress.ItemDone(sr.SourcePath, sr.SummaryPath)

		// Update manifest — refresh both Hash and Type so config-driven type
		// changes propagate on recompile of existing entries.
		for _, s := range sources {
			if s.Path == sr.SourcePath {
				if _, exists := mf.Sources[sr.SourcePath]; !exists {
					mf.AddSource(s.Path, s.Hash, s.Type, s.Size)
				} else {
					src := mf.Sources[sr.SourcePath]
					src.Hash = s.Hash
					src.Type = s.Type
					mf.Sources[sr.SourcePath] = src
				}
				break
			}
		}
		mf.MarkCompiled(sr.SourcePath, sr.SummaryPath, sr.Concepts)

		// Index in FTS5
		opts.MemStore.Add(memory.Entry{
			ID:          sr.SourcePath,
			Content:     sr.Summary,
			Tags:        []string{TypeForFile(opts.ProjectDir, sr.SourcePath, cfg)},
			ArticlePath: sr.SummaryPath,
		})
		sourcedate.RecordForSource(opts.MemStore, opts.ProjectDir, sr.SourcePath, mf.Sources[sr.SourcePath].AddedAt)

		// Generate embedding
		if opts.Embedder != nil {
			vec, err := opts.Embedder.Embed(sr.Summary)
			if err != nil {
				log.Warn("embedding failed", "source", sr.SourcePath, "error", err)
				result.EmbedErrors++
			} else {
				opts.VecStore.Upsert(sr.SourcePath, vec)
			}
		}
	}

	client.TeardownCache(sumCacheID)

	// Budget guard: stop between passes when MaxCost is exceeded.
	if budgetExceeded(opts.Tracker, opts.MaxCost) {
		log.Info("MaxCost guard: stopping after summarize pass")
		result.BudgetExhausted = true
		return result
	}

	// Pass 2: Concept extraction
	successfulSummaries := filterSuccessful(summaries)
	if len(successfulSummaries) == 0 {
		return result
	}

	extractModel := cfg.Models.Extract
	if extractModel == "" {
		extractModel = model
	}

	client.SetPass("extract")
	var extCacheID string
	if opts.CacheEnabled {
		extCacheID, _ = client.SetupCache("You are an expert knowledge organizer. Extract structured concepts from source summaries.", extractModel)
	}
	progress.StartPhase("Pass 2: Extract concepts", len(successfulSummaries))
	concepts, err := ExtractConcepts(opts.Ctx, successfulSummaries, mf.Concepts, client, extractModel, cfg.Compiler.ExtractBatchSize, cfg.Compiler.ExtractMaxTokens, cfg.Compiler.MaxParallel, opts.Prompts, cfg.Compiler.CompileTemperature())
	if err != nil {
		progress.ItemError("concept extraction", err)
		result.Errors++
		progress.EndPhase()
		client.TeardownCache(extCacheID)
		return result
	}

	// Budget guard: stop between passes when MaxCost is exceeded.
	if budgetExceeded(opts.Tracker, opts.MaxCost) {
		log.Info("MaxCost guard: stopping after extract pass")
		progress.EndPhase()
		result.BudgetExhausted = true
		return result
	}

	// Embedding-based deduplication (if embedder available and strategy != "llm")
	if opts.Embedder != nil && cfg.Compiler.DedupStrategy != "llm" {
		dc := NewDedupCache(opts.Embedder, opts.VecStore, cfg.Compiler.DedupThreshold)

		// Seed with existing concepts (SPEC-04 D1: sorted — seed order must
		// not depend on map iteration)
		dc.Seed(sortedConceptNames(mf.Concepts))

		// Check new concepts for duplicates
		var dedupedConcepts []ExtractedConcept
		merged := 0
		for _, c := range concepts {
			match, score, vec := dc.CheckDuplicate(c.Name)
			if match != "" {
				log.Info("concept dedup: merging", "new", c.Name, "existing", match, "score", score)
				// Merge sources into existing concept (deduplicate source list)
				mergeConceptIntoManifest(mf, match, c)
				merged++
				continue
			}
			// Use pre-computed vec from CheckDuplicate to avoid double-embed
			if vec != nil {
				dc.AddWithVec(c.Name, vec)
			} else {
				dc.Add(c.Name)
			}
			dedupedConcepts = append(dedupedConcepts, c)
		}

		if merged > 0 {
			log.Info("concept dedup complete", "original", len(concepts), "merged", merged, "remaining", len(dedupedConcepts))
		}
		concepts = dedupedConcepts
	}

	// Evidence gate (#128): source-less concepts are suppressed entirely —
	// no manifest entry, no article, no entity, no cites.
	concepts, _ = filterLowEvidence(concepts, cfg.Compiler.MinConceptSourcesOrDefault())
	result.ConceptsExtracted = len(concepts)

	var conceptNames []string
	for _, c := range concepts {
		conceptNames = append(conceptNames, c.Name)
		mf.AddConcept(c.Name, filepath.ToSlash(filepath.Join(cfg.Output, "concepts", c.Name+".md")), c.Sources, c.Aliases...)
	}
	progress.ConceptsDiscovered(conceptNames)
	progress.EndPhase()
	client.TeardownCache(extCacheID)

	// Hoisted from below (was immediately before Pass 3) so the deferred
	// resolution pass can capture the store Pass 3 actually writes through.
	// Capturing opts.OntStore instead would hand resolution a nil store on
	// exactly the path where Pass 3 built its own — and the pass would silently
	// return. Moved as a unit: the fallback references merged/mergedTypes.
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	writeOntStore := opts.OntStore
	if writeOntStore == nil {
		writeOntStore = ontology.NewStore(opts.DB, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
		bindOntSink(writeOntStore, opts.Sink, opts.ProjectDir)
	}

	// Pass 2b: LLM triple extraction (P3-2, opt-in). Runs BEFORE the
	// zero-concept early return below — otherwise triples would silently never
	// persist on an incremental compile where every concept dedup-merged, which
	// is the ordinary case. Never fails the compile; see ExtractTriplesPass.
	touched, supersessions, tripleTimeouts := ExtractTriplesPass(opts.Ctx, writeOntStore, successfulSummaries, concepts, cfg, client, false, opts.ProjectDir, mf, opts.TrustStore, opts.Prompts, opts.Budgets, opts.Sink)
	// SPEC-08 AC11: a per-doc budget expiry in the triples leg is a typed
	// timeout — emit the event pair (compile_doc_finished{Skipped:true} +
	// limit_exceeded) and force the run incomplete so the doc re-enters the
	// next compile (its summary persisted, so only an incomplete run
	// reprocesses it). The pass lacks JobID, so the emission lands here.
	hadTripleTimeout := false
	for _, t := range tripleTimeouts {
		hadTripleTimeout = true
		emitDocTimeout(opts.Sink, opts.ProjectDir, opts.JobID, t.SourcePath, t.Err)
		progress.ItemError(t.SourcePath, t.Err)
	}

	// Pass 4: entity resolution (P3-3, opt-in). Deferred rather than called
	// inline because it must run AFTER Pass 3 — WriteArticles is what creates
	// concept entity rows and their cites edges — while still covering the
	// zero-concept early return below, which skips Pass 3 entirely but has
	// triple entities to resolve. One registration, every exit path; the
	// alternative is duplicating the call at two returns, where the next edit
	// adds a third. The closure reads `touched` at return time, so Pass 3's
	// contribution is included.
	defer func() {
		ResolveEntitiesPass(opts.Ctx, writeOntStore, touched, cfg, client, opts.Embedder, opts.Prompts,
			events.BindWorkspace(opts.Sink, filepath.Base(opts.ProjectDir)))
		// Second supersession trigger (P3-6): links applied above may have
		// created alias forms the write-time trigger could not see.
		runSupersessionSweep(writeOntStore, supersessions)
		// Community detection (P3-5): runs last, on the final graph. A nil
		// CommunityStore (a store that predates P3-5) disables the pass.
		cs, _ := writeOntStore.(store.CommunityStore)
		if cs != nil {
			CommunitiesPass(opts.Ctx, opts.ProjectDir, writeOntStore, cs, opts.MemStore, opts.VecStore, opts.Embedder, cfg, client)
		}
	}()

	// Pass 3: Write articles
	if len(concepts) == 0 {
		// Extraction COMPLETED — it just produced no new concepts to write (e.g. an
		// incremental compile where every concept dedup-merged into an existing
		// one). Pass 2/3 are done for this run, so mark it completed (unless
		// cancelled). Leaving it false here would make callers roll these sources
		// out of the manifest and re-summarize them on every compile, forever. P1-1.
		result.Pass23Completed = (opts.Ctx == nil || opts.Ctx.Err() == nil) && !hadTripleTimeout
		return result
	}

	writeModel := cfg.Models.Write
	if writeModel == "" {
		writeModel = model
	}
	articleMaxTokens := cfg.Compiler.ArticleMaxTokens
	if articleMaxTokens <= 0 {
		articleMaxTokens = 4000
	}

	client.SetPass("write")
	var writeCacheID string
	if opts.CacheEnabled {
		writeCacheID, _ = client.SetupCache("You are a knowledge base article writer. Write comprehensive, well-structured wiki articles.", writeModel)
	}
	relPatterns := ontology.RelationPatterns(merged)
	progress.StartPhase("Pass 3: Write articles", len(concepts))
	articles := WriteArticles(ArticleWriteOpts{
		Temperature:            cfg.Compiler.CompileTemperature(),
		Prompts:                opts.Prompts,
		Ctx:                    opts.Ctx,
		ProjectDir:             opts.ProjectDir,
		OutputDir:              cfg.Output,
		Client:                 client,
		Model:                  writeModel,
		MaxTokens:              articleMaxTokens,
		MaxParallel:            cfg.Compiler.MaxParallel,
		MemStore:               opts.MemStore,
		VecStore:               opts.VecStore,
		OntStore:               writeOntStore,
		ChunkStore:             opts.ChunkStore,
		DB:                     opts.DB,
		Embedder:               opts.Embedder,
		UserTZ:                 cfg.Compiler.UserTimeLocation(),
		ArticleFields:          cfg.Compiler.ArticleFields,
		RelationPatterns:       relPatterns,
		ChunkSize:              cfg.Search.ChunkSizeOrDefault(),
		ChunkOverlap:           cfg.Search.ChunkOverlapOrDefault(),
		SplitThreshold:         cfg.Compiler.SplitThreshold,
		MaxSourceContextTokens: cfg.Compiler.MaxSourceContextTokensOrDefault(),
		Language:               cfg.Language,
		Backpressure:           opts.Backpressure,
		AntiPatternPhrases:     cfg.Compiler.AntiPatternPhrasesOrDefault(),
		AllConcepts:            manifestConceptRefs(mf.Concepts),
	}, concepts)

	// Pass 3's contribution to the resolution set. concept.Name IS the entity id
	// WriteArticles wrote (write.go). ar.Error == nil does not strictly prove the
	// entity landed — writeOneArticle logs an AddEntity failure without failing
	// the article — so resolvableSeeds re-checks membership in the pool and drops
	// anything absent. The deferred pass reads `touched` at return time, so
	// appending here lands before it runs.
	for _, ar := range articles {
		// Skip unlaunched/cancelled slots (zero-value results — review F5):
		// an empty ConceptName means the article never ran, not that it
		// succeeded with an empty name.
		if ar.Error == nil && ar.ConceptName != "" {
			touched = append(touched, ar.ConceptName)
		}
	}

	// Quality scoring config (issue #97): weights + warning threshold.
	wf, wg, wc, ww, wa := cfg.Compiler.QualityWeights()
	qualityWeights := QualityWeights{Format: wf, Grounding: wg, Coverage: wc, Wikilink: ww, AntiPattern: wa}
	qualityThreshold := cfg.Compiler.QualityThreshold()
	belowThreshold := 0

	for _, ar := range articles {
		if ar.ConceptName == "" {
			continue // unlaunched/cancelled slot (review F5) — not an error, not written
		}
		if ar.Error != nil {
			result.Errors++
			progress.ItemError(ar.ConceptName, ar.Error)
		} else {
			result.ArticlesWritten++
			progress.ItemDone(ar.ConceptName, ar.ArticlePath)

			// Per-article quality scoring
			if opts.ItemStore != nil {
				articlePath := filepath.Join(opts.ProjectDir, ar.ArticlePath)
				articleContent, readErr := os.ReadFile(articlePath)
				if readErr != nil {
					// Don't score an unreadable article — an empty read would
					// produce a spurious near-zero score and false low-quality
					// warning. Surface the error and skip scoring (no silent fail).
					log.Warn("quality scoring skipped: read article failed",
						"concept", ar.ConceptName, "path", ar.ArticlePath, "error", readErr)
					continue
				}

				// Read source content for coverage scoring
				var sourceText string
				if concept, ok := mf.Concepts[ar.ConceptName]; ok {
					for _, srcPath := range concept.Sources {
						data, err := os.ReadFile(filepath.Join(opts.ProjectDir, srcPath))
						if err == nil {
							sourceText += string(data) + "\n"
						}
					}
				}

				scores := ScoreArticle(string(articleContent), sourceText, ar.ConceptName, mf, qualityWeights)
				for _, srcPath := range mf.Concepts[ar.ConceptName].Sources {
					if err := opts.ItemStore.SetQualityScore(srcPath, scores.Combined); err != nil {
						log.Warn("set quality score failed", "path", srcPath, "error", err)
					}
				}

				if scores.Combined < qualityThreshold {
					belowThreshold++
					log.Warn("low quality article", "concept", ar.ConceptName,
						"score", scores.Combined, "format", scores.Format,
						"grounding", scores.Grounding, "coverage", scores.Coverage,
						"wikilink", scores.Wikilink, "antipattern", scores.AntiPattern)
				}
			}
		}
	}

	// Compile-end summary: surface how many articles fell below the threshold.
	if belowThreshold > 0 {
		log.Warn("articles below quality threshold",
			"count", belowThreshold, "total", result.ArticlesWritten, "threshold", qualityThreshold)
	}

	progress.EndPhase()
	client.TeardownCache(writeCacheID)

	// Pass 2/3 ran to the end here. Only now — not on the extraction-failure early
	// returns above, and not if the run was cancelled — are the summarize-succeeded
	// sources safe to mark extracted/written in the checkpoints. Callers gate their
	// pass-marking on this so an interrupted/failed run stays resumable. P1-1.
	result.Pass23Completed = (opts.Ctx == nil || opts.Ctx.Err() == nil) && !hadTripleTimeout

	return result
}
