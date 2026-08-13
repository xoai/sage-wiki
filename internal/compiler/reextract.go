package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// ReExtract re-runs Pass 2 (concept extraction) and Pass 3 (article writing)
// using existing summaries from wiki/summaries/. Skips Pass 0 and Pass 1.
// ReExtractOption customizes a ReExtract run.
type ReExtractOption func(*reExtractOpts)

type reExtractOpts struct {
	prompts *prompts.Registry
}

// WithPrompts runs the re-extract against a per-workspace template
// registry (SPEC-01) instead of the process-global prompts default.
func WithPrompts(r *prompts.Registry) ReExtractOption {
	return func(o *reExtractOpts) { o.prompts = r }
}

func ReExtract(projectDir string, options ...ReExtractOption) (*CompileResult, error) {
	result := &CompileResult{}
	var ro reExtractOpts
	for _, opt := range options {
		opt(&ro)
	}

	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: load config: %w", err)
	}

	// Load user prompt overrides if prompts/ directory exists — into the
	// supplied registry, else the process-global default.
	if ro.prompts != nil {
		if err := ro.prompts.LoadFromDir(filepath.Join(projectDir, "prompts")); err != nil {
			log.Warn("failed to load custom prompts", "error", err)
		}
	} else if err := prompts.LoadFromDir(filepath.Join(projectDir, "prompts")); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}

	mf, err := manifest.Load(filepath.Join(projectDir, ".manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: load manifest: %w", err)
	}
	mf.SetNow(config.NowUTC)
	// Merge base (D3): snapshot before mutation so the Save reload-merges the
	// re-extract run's delta onto any writer that landed during it.
	base := mf.Clone()

	// Read existing summaries from disk
	summaryDir := filepath.Join(projectDir, cfg.Output, "summaries")
	entries, err := os.ReadDir(summaryDir)
	if err != nil {
		return nil, fmt.Errorf("re-extract: no summaries found at %s: %w", summaryDir, err)
	}

	var summaries []SummaryResult
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(summaryDir, e.Name()))
		if err != nil {
			continue
		}
		summaries = append(summaries, SummaryResult{
			SourcePath:  e.Name(),
			SummaryPath: filepath.ToSlash(filepath.Join(cfg.Output, "summaries", e.Name())),
			Summary:     string(data),
		})
	}

	log.Info("re-extract: loaded existing summaries", "count", len(summaries))

	if len(summaries) == 0 {
		return result, fmt.Errorf("re-extract: no summaries found — run sage-wiki compile first")
	}

	// Create LLM client
	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("re-extract: create LLM client: %w", err)
	}

	// Open DB
	// P2-1 skip-list: import cycle (sqlitestore imports compiler) — backend
	// selection arrives in T9a via caller-injected interface.
	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes),
		ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)

	// Pass 2: Concept extraction
	extractModel := cfg.Models.Extract
	if extractModel == "" {
		extractModel = cfg.Models.Summarize
	}

	log.Info("Pass 2: extracting concepts", "from_summaries", len(summaries))
	// ReExtract has no cancellation context of its own; --re-extract cancellation
	// is a follow-up. Use a background context so the LLM calls still function.
	concepts, err := ExtractConcepts(context.Background(), summaries, mf.Concepts, client, extractModel, cfg.Compiler.ExtractBatchSize, cfg.Compiler.ExtractMaxTokens, cfg.Compiler.MaxParallel, ro.prompts, cfg.Compiler.CompileTemperature())
	if err != nil {
		return nil, fmt.Errorf("re-extract: concept extraction: %w", err)
	}
	// Evidence gate (#128): source-less concepts are suppressed entirely.
	concepts, _ = filterLowEvidence(concepts, cfg.Compiler.MinConceptSourcesOrDefault())
	result.ConceptsExtracted = len(concepts)

	// Update manifest with concepts
	for _, c := range concepts {
		mf.AddConcept(c.Name, filepath.ToSlash(filepath.Join(cfg.Output, "concepts", c.Name+".md")), c.Sources, c.Aliases...)
	}

	// Pass 2b: LLM triple extraction (P3-2, opt-in). summariesCarryFrontmatter
	// is true here: this path loads summary FILES from disk, so Summary holds
	// the frontmatter and SourcePath is the summary's filename, not the source
	// document.
	touched, supersessions, _ := ExtractTriplesPass(context.Background(), ontStore, summaries, concepts, cfg, client, true, projectDir, mf, trust.NewStore(db), ro.prompts, nil, nil)

	// Pass 3: Write articles
	if len(concepts) > 0 {
		writeModel := cfg.Models.Write
		if writeModel == "" {
			writeModel = extractModel
		}
		articleMaxTokens := cfg.Compiler.ArticleMaxTokens
		if articleMaxTokens <= 0 {
			articleMaxTokens = 4000
		}

		relPatterns := ontology.RelationPatterns(mergedRels)
		log.Info("Pass 3: writing articles", "concepts", len(concepts))
		articles := WriteArticles(ArticleWriteOpts{
			Temperature:            cfg.Compiler.CompileTemperature(),
			Prompts:                ro.prompts,
			ProjectDir:             projectDir,
			OutputDir:              cfg.Output,
			Client:                 client,
			Model:                  writeModel,
			MaxTokens:              articleMaxTokens,
			MaxParallel:            cfg.Compiler.MaxParallel,
			MemStore:               memStore,
			VecStore:               vecStore,
			OntStore:               ontStore,
			ChunkStore:             chunkStore,
			DB:                     db,
			Embedder:               embedder,
			UserTZ:                 cfg.Compiler.UserTimeLocation(),
			ArticleFields:          cfg.Compiler.ArticleFields,
			RelationPatterns:       relPatterns,
			ChunkSize:              cfg.Search.ChunkSizeOrDefault(),
			ChunkOverlap:           cfg.Search.ChunkOverlapOrDefault(),
			Language:               cfg.Language,
			AntiPatternPhrases:     cfg.Compiler.AntiPatternPhrasesOrDefault(),
			MaxSourceContextTokens: cfg.Compiler.MaxSourceContextTokensOrDefault(),
			AllConcepts:            manifestConceptRefs(mf.Concepts),
		}, concepts)

		for _, ar := range articles {
			if ar.Error != nil {
				result.Errors++
			} else {
				result.ArticlesWritten++
				// concept.Name IS the entity id WriteArticles wrote; only
				// successful articles produced a row.
				touched = append(touched, ar.ConceptName)
			}
		}
	}

	// Pass 4: entity resolution (P3-3, opt-in). Placed after the Pass 3 block
	// rather than deferred: unlike runFullPipeline this path has no early
	// return between here and Pass 3, so one call covers both branches.
	// ReExtract has no cancellation context of its own, matching the calls
	// above.
	ResolveEntitiesPass(context.Background(), ontStore, touched, cfg, client, embedder, ro.prompts)
	// Second supersession trigger (P3-6): same pre-resolution hazard as the
	// full pipeline — without it this path has no self-heal until a full
	// compile.
	runSupersessionSweep(ontStore, supersessions)

	// Community detection (P3-5): runs last, on the final graph. The
	// concrete store implements both interfaces.
	CommunitiesPass(context.Background(), projectDir, ontStore, ontStore, memStore, vecStore, embedder, cfg, client)

	// Post-compile sweep: strip [[wikilinks]] pointing at concepts that don't
	// exist on disk. Re-extract rewrites articles via Pass 3 and would
	// otherwise leave phantom links in place — same problem the strip pass
	// solves for the main Compile() path. Issue #94 (follow-up to #90).
	MaybeStripBrokenWikilinks(projectDir, cfg.Output, cfg.Compiler.StripBrokenLinksEnabled(), memStore)

	// Save manifest via reload-merge under the lock (D3) so a concurrent short
	// writer is preserved. ReExtract has no cancellation context of its own
	// (see the ExtractConcepts call above), so use a background context.
	if err := manifest.MergeSave(context.Background(), filepath.Join(projectDir, ".manifest.json"), base, mf); err != nil {
		return nil, fmt.Errorf("re-extract: save manifest: %w", err)
	}

	log.Info("re-extract complete", "concepts", result.ConceptsExtracted, "articles", result.ArticlesWritten, "errors", result.Errors)
	return result, nil
}
