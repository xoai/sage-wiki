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
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// OnDemandOpts configures a compile-on-demand request.
type OnDemandOpts struct {
	Topic       string
	MaxSources  int // default 20
	ProjectDir  string
	Config      *config.Config
	DB          *storage.DB
	Searcher    *hybrid.Searcher
	Embedder    embed.Embedder
	Client      *llm.Client
	Coordinator *CompileCoordinator
}

// OnDemandResult summarizes what compile-on-demand produced.
type OnDemandResult struct {
	CompiledSources   int            `json:"compiled_sources"`
	ArticlesWritten   int            `json:"articles_written"`
	ConceptsExtracted int            `json:"concepts_extracted"`
	DurationSeconds   float64        `json:"duration_seconds"`
	Articles          []ArticleInfo  `json:"articles,omitempty"`
	Message           string         `json:"message,omitempty"` // status message (e.g., "compile in progress")
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
	items := NewCompileItemStore(opts.DB)
	var uncompiled []SourceInfo
	seen := make(map[string]bool)

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

		uncompiled = append(uncompiled, SourceInfo{
			Path:     item.SourcePath,
			Hash:     item.Hash,
			Type:     item.FileType,
			Size:     item.SizeBytes,
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
		ontStore := ontology.NewStore(opts.DB, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))

		mfPath := filepath.Join(opts.ProjectDir, ".manifest.json")
		mf, err := manifest.Load(mfPath)
		if err != nil {
			return fmt.Errorf("on-demand: load manifest: %w", err)
		}

		bp := NewBackpressureController(cfg.Compiler.MaxParallel)
		cacheEnabled := cfg.Compiler.PromptCacheEnabled()

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
			Embedder:     opts.Embedder,
			Backpressure: bp,
			ItemStore:    items,
			CacheEnabled: cacheEnabled,
			Progress:     NewProgress(),
		})

		result.ArticlesWritten = pResult.ArticlesWritten
		result.ConceptsExtracted = pResult.ConceptsExtracted

		// Collect written article info
		for name, concept := range mf.Concepts {
			for _, src := range uncompiled {
				for _, cs := range concept.Sources {
					if cs == src.Path {
						result.Articles = append(result.Articles, ArticleInfo{
							Name: name,
							Path: concept.ArticlePath,
						})
					}
				}
			}
		}

		// Mark passes only for sources that succeeded
		succeeded := make(map[string]bool)
		for _, p := range pResult.SucceededSources {
			succeeded[p] = true
		}
		// Mark extract/write only when Pass 2/3 completed (not cancelled, not a
		// total-extraction failure) so an interrupted/failed run stays resumable. P1-1.
		runCompleted := pResult.Pass23Completed
		for _, src := range uncompiled {
			if succeeded[src.Path] {
				if err := items.MarkPass(src.Path, "summarized"); err != nil {
					log.Warn("on-demand: mark pass failed", "path", src.Path, "pass", "summarized", "error", err)
				}
				if runCompleted {
					if err := items.MarkPass(src.Path, "extracted"); err != nil {
						log.Warn("on-demand: mark pass failed", "path", src.Path, "pass", "extracted", "error", err)
					}
					if err := items.MarkPass(src.Path, "written"); err != nil {
						log.Warn("on-demand: mark pass failed", "path", src.Path, "pass", "written", "error", err)
					}
				}
			}
		}

		// When Pass 2/3 did not complete, drop the summarize-succeeded sources from
		// the manifest so the next Diff re-includes them (their extract/write pass
		// flags stayed 0), else Pass 1's AddSource makes the Diff empty and resume
		// silently skips their concept/article passes. P1-1 / C1.
		if !runCompleted {
			for _, p := range pResult.SucceededSources {
				mf.RemoveSource(p)
			}
		}

		// Save manifest
		if err := mf.Save(mfPath); err != nil {
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
