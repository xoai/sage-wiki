package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/events"
)

// OnDemandOpts configures a compile-on-demand request.
type OnDemandOpts struct {
	Topic      string
	MaxSources int // default 20
	ProjectDir string
	Config     *config.Config
	DB         store.DBHandle
	// TrustStore is optional (P3-6): pass the Backend's Trust store under
	// storage.backend=postgres; nil falls back to the sqlite implementation.
	TrustStore  store.TrustStore
	Searcher    *hybrid.Searcher
	Embedder    embed.Embedder
	Client      *llm.Client
	Coordinator *CompileCoordinator
	// Prompts, when set, is the per-workspace template registry (SPEC-01);
	// nil = the prompts package default. The caller owns loading overrides.
	Prompts *prompts.Registry
	// Sink, when set, receives the on-demand events (SPEC-07):
	// promotion_triggered for each promoted source, plus the pipeline-
	// interior events of the triggered run (edge, entity, usage). The
	// run itself is NOT bracketed — compile_started/finished belong to
	// the Compile/job boundaries (CLI runTiers, serve worker cycles).
	Sink events.Sink
}

// OnDemandResult summarizes what compile-on-demand produced.
type OnDemandResult struct {
	CompiledSources   int           `json:"compiled_sources"`
	ArticlesWritten   int           `json:"articles_written"`
	ConceptsExtracted int           `json:"concepts_extracted"`
	DurationSeconds   float64       `json:"duration_seconds"`
	Articles          []ArticleInfo `json:"articles,omitempty"`
	Message           string        `json:"message,omitempty"` // status message (e.g., "compile in progress")
}

// ArticleInfo describes a newly written article.
type ArticleInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// CompileTopic finds uncompiled sources for a topic and runs the full pipeline.
// Uses the CompileCoordinator to serialize with background compiles.
func CompileTopic(ctx context.Context, opts OnDemandOpts) (*OnDemandResult, error) {
	start := time.Now()

	if opts.MaxSources <= 0 {
		opts.MaxSources = 20
	}

	// Search for relevant sources
	var queryVec []float32
	if opts.Embedder != nil {
		vec, err := opts.Embedder.Embed(opts.Topic)
		if err != nil {
			log.Warn("on-demand: embed query failed, using BM25 only", "error", err)
		} else {
			queryVec = vec
		}
	}

	searchResults, err := opts.Searcher.Search(hybrid.SearchOpts{
		Query: opts.Topic,
		Limit: opts.MaxSources * 2, // search wider, filter below
	}, queryVec)
	if err != nil {
		return nil, fmt.Errorf("on-demand: search: %w", err)
	}

	// Filter to uncompiled sources (Tier < 3)
	items := NewCompileItemStore(opts.DB, config.NowUTC)
	var uncompiled []SourceInfo
	seen := make(map[string]bool)
	fromTiers := make(map[string]int) // path → tier before promotion (SPEC-07 event)

	for _, r := range searchResults {
		path := r.ID
		// Strip "src:" prefix if present
		if len(path) > 4 && path[:4] == "src:" {
			path = path[4:]
		}
		if seen[path] {
			continue
		}
		seen[path] = true

		item, _ := items.GetByPath(path)
		if item == nil {
			continue
		}
		if item.Tier >= 3 && item.PassWritten {
			continue // already fully compiled
		}

		fromTiers[item.SourcePath] = item.Tier
		uncompiled = append(uncompiled, SourceInfo{
			Path: item.SourcePath,
			Hash: item.Hash,
			Type: item.FileType,
			Size: item.SizeBytes,
		})

		if len(uncompiled) >= opts.MaxSources {
			break
		}
	}

	if len(uncompiled) == 0 {
		return &OnDemandResult{
			DurationSeconds: time.Since(start).Seconds(),
			Message:         "All matching sources are already compiled.",
		}, nil
	}

	// Promote to Tier 3
	for _, src := range uncompiled {
		if err := items.SetTier(src.Path, 3, "on-demand: "+opts.Topic); err != nil {
			log.Warn("on-demand: set tier failed", "path", src.Path, "error", err)
			continue
		}
		if opts.Sink != nil {
			opts.Sink.Emit(events.NewEvent(filepath.Base(opts.ProjectDir), events.TypePromotionTriggered, events.PromotionTriggered{
				DocID:    src.Path,
				FromTier: fromTiers[src.Path],
				ToTier:   3,
				Trigger:  "compile-on-demand",
			}))
		}
	}

	// Run full pipeline via coordinator
	result := &OnDemandResult{CompiledSources: len(uncompiled)}

	compileFn := func() error {
		cfg := opts.Config

		memStore := memory.NewStore(opts.DB)
		vecStore := vectors.NewStore(opts.DB)
		chunkStore := memory.NewChunkStore(opts.DB)
		merged := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		ontStore := ontology.NewStore(opts.DB, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
		bindOntSink(ontStore, opts.Sink, opts.ProjectDir)

		mfPath := filepath.Join(opts.ProjectDir, ".manifest.json")
		mf, err := manifest.Load(mfPath)
		if err != nil {
			return fmt.Errorf("on-demand: load manifest: %w", err)
		}
		mf.SetNow(config.NowUTC)
		// Merge base (D3): snapshot before mutation so the Save reload-merges the
		// on-demand run's delta onto any writer that landed during it.
		base := mf.Clone()

		bp := NewBackpressureController(cfg.Compiler.MaxParallel)
		cacheEnabled := cfg.Compiler.PromptCacheEnabled()
		trustStore := opts.TrustStore
		if trustStore == nil {
			trustStore = trust.NewStore(opts.DB)
		}

		// SPEC-07 metrics: an on-demand run IS a tier-3 compile job —
		// it records the same counter/histogram as other compile paths.
		compileStart := time.Now()
		pResult := runFullPipeline(uncompiled, FullPipelineOpts{
			Ctx:          ctx,
			ProjectDir:   opts.ProjectDir,
			Config:       cfg,
			Client:       opts.Client,
			Manifest:     mf,
			DB:           opts.DB,
			MemStore:     memStore,
			VecStore:     vecStore,
			ChunkStore:   chunkStore,
			OntStore:     ontStore,
			TrustStore:   trustStore,
			Embedder:     opts.Embedder,
			Prompts:      opts.Prompts,
			Backpressure: bp,
			ItemStore:    items,
			CacheEnabled: cacheEnabled,
			Progress:     NewProgress(),
			Sink:         opts.Sink,
		})

		outcome := "completed"
		if !pResult.Pass23Completed {
			outcome = "failed"
		}
		metrics.CounterNamed("compiles_total", "tier", "3", "outcome", outcome).Add(1)
		metrics.ObserveDuration(metrics.HistogramNamed("compile_duration_seconds", metrics.CompileBuckets(), "tier", "3"), compileStart)

		result.ArticlesWritten = pResult.ArticlesWritten
		result.ConceptsExtracted = pResult.ConceptsExtracted

		// Collect written article info (SPEC-04 D1: sorted by name, deduped —
		// a concept matches once per matching source without the guard)
		seenArticles := map[string]bool{}
		for _, name := range sortedConceptNames(mf.Concepts) {
			concept := mf.Concepts[name]
			for _, src := range uncompiled {
				for _, cs := range concept.Sources {
					if cs == src.Path && !seenArticles[name] {
						seenArticles[name] = true
						result.Articles = append(result.Articles, ArticleInfo{
							Name: name,
							Path: concept.ArticlePath,
						})
					}
				}
			}
		}

		// When Pass 2/3 did not complete (cancel or total-extraction failure), this
		// run is incomplete: mark nothing and skip the manifest Save, persisting no
		// new compile state. The in-memory AddSource/MarkCompiled/AddConcept mutations
		// are discarded, so the next compile's Diff re-includes these sources and
		// reprocesses them cleanly. The earlier surgical RemoveSource dropped only the
		// sources, leaving the run's concepts orphaned (RemoveSource deletes Sources
		// only). P1-1 / C1.
		if !pResult.Pass23Completed {
			log.Info("on-demand compile interrupted before Pass 2/3 completed — manifest not saved; sources will reprocess")
			return nil
		}

		// Pass 2/3 completed — advance all three pass flags for the summarize-
		// succeeded sources so ListPending treats them as fully compiled.
		succeeded := make(map[string]bool)
		for _, p := range pResult.SucceededSources {
			succeeded[p] = true
		}
		for _, src := range uncompiled {
			if succeeded[src.Path] {
				for _, pass := range []string{"summarized", "extracted", "written"} {
					if err := items.MarkPass(src.Path, pass); err != nil {
						log.Warn("on-demand: mark pass failed", "path", src.Path, "pass", pass, "error", err)
					}
				}
			}
		}

		// Save manifest via reload-merge under the lock (D3) so a concurrent short
		// writer is preserved. Stays after the Pass23-incomplete early-return above,
		// mirroring the compile placement invariant.
		if err := manifest.MergeSave(orBackground(ctx), mfPath, base, mf); err != nil {
			return fmt.Errorf("on-demand: save manifest: %w", err)
		}

		return nil
	}

	if opts.Coordinator != nil {
		err = opts.Coordinator.CompileOrWait(ctx, compileFn)
		if err == ErrCompileTimeout {
			result.Message = "Compilation in progress. Results are from indexed sources only."
			result.CompiledSources = 0
			return result, nil
		}
	} else {
		err = compileFn()
	}

	if err != nil {
		return nil, err
	}

	result.DurationSeconds = time.Since(start).Seconds()
	return result, nil
}
