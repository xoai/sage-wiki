package compiler

import (
	"context"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// FullPipelineOpts bundles all parameters needed for the full compilation
// pipeline (Pass 1 → Pass 2 → Pass 3).
type FullPipelineOpts struct {
	Ctx          context.Context // cancellation context; nil = background
	ProjectDir   string
	Config       *config.Config
	Client       *llm.Client
	Manifest     *manifest.Manifest
	DB           *storage.DB
	MemStore     *memory.Store
	VecStore     *vectors.Store
	ChunkStore   *memory.ChunkStore
	OntStore     *ontology.Store
	Embedder     embed.Embedder
	Backpressure *BackpressureController
	ItemStore    *CompileItemStore // optional — for per-article quality scoring
	CacheEnabled bool
	Progress     *Progress

	// Checkpoint (legacy — retained alongside compile_items for M3 fallback)
	State     *CompileState
	StatePath string
}

// FullPipelineResult summarizes what the full pipeline produced.
type FullPipelineResult struct {
	Summarized        int
	ConceptsExtracted int
	ArticlesWritten   int
	Errors            int
	EmbedErrors       int
	SucceededSources  []string // source paths that completed summarization successfully
}

// runFullPipeline executes Pass 1 (summarize) → Pass 2 (extract) → Pass 3 (write)
// on the given sources. This is the existing LLM compilation pipeline, extracted
// from Compile() for reuse by both the tiered orchestrator and compile-on-demand.
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

	summaries := Summarize(SummarizeOpts{
		Ctx:          opts.Ctx,
		ProjectDir:   opts.ProjectDir,
		OutputDir:    cfg.Output,
		Sources:      sources,
		Client:       client,
		Model:        model,
		MaxTokens:    maxTokens,
		MaxParallel:  cfg.Compiler.MaxParallel,
		UserTZ:       cfg.Compiler.UserTimeLocation(),
		Language:     cfg.Language,
		Backpressure: opts.Backpressure,
	})

	for _, sr := range summaries {
		if sr.Error != nil {
			result.Errors++
			progress.ItemError(sr.SourcePath, sr.Error)
			if opts.State != nil {
				opts.State.Failed = append(opts.State.Failed, FailedSource{
					Path:  sr.SourcePath,
					Error: sr.Error.Error(),
				})
			}
			continue
		}

		result.Summarized++
		result.SucceededSources = append(result.SucceededSources, sr.SourcePath)
		progress.ItemDone(sr.SourcePath, sr.SummaryPath)

		// Update manifest
		for _, s := range sources {
			if s.Path == sr.SourcePath {
				if _, exists := mf.Sources[sr.SourcePath]; !exists {
					mf.AddSource(s.Path, s.Hash, s.Type, s.Size)
				} else {
					src := mf.Sources[sr.SourcePath]
					src.Hash = s.Hash
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
			Tags:        []string{extractType(sr.SourcePath, cfg.TypeSignals)},
			ArticlePath: sr.SummaryPath,
		})

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

		// Update legacy checkpoint
		if opts.State != nil {
			removeFromPending(opts.State, sr.SourcePath)
			opts.State.Completed = append(opts.State.Completed, sr.SourcePath)
			if opts.StatePath != "" {
				saveCompileState(opts.StatePath, opts.State)
			}
		}
	}

	client.TeardownCache(sumCacheID)
	if opts.State != nil {
		opts.State.Pass = 2
		if opts.StatePath != "" {
			saveCompileState(opts.StatePath, opts.State)
		}
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
	concepts, err := ExtractConcepts(successfulSummaries, mf.Concepts, client, extractModel, cfg.Compiler.ExtractBatchSize, cfg.Compiler.ExtractMaxTokens, cfg.Compiler.MaxParallel)
	if err != nil {
		progress.ItemError("concept extraction", err)
		result.Errors++
		return result
	}

	// Embedding-based deduplication (if embedder available and strategy != "llm")
	if opts.Embedder != nil && cfg.Compiler.DedupStrategy != "llm" {
		dc := NewDedupCache(opts.Embedder, opts.VecStore, cfg.Compiler.DedupThreshold)

		// Seed with existing concepts
		var existingNames []string
		for name := range mf.Concepts {
			existingNames = append(existingNames, name)
		}
		dc.Seed(existingNames)

		// Check new concepts for duplicates
		var dedupedConcepts []ExtractedConcept
		merged := 0
		for _, c := range concepts {
			match, score, vec := dc.CheckDuplicate(c.Name)
			if match != "" {
				log.Info("concept dedup: merging", "new", c.Name, "existing", match, "score", score)
				// Merge sources into existing concept (deduplicate source list)
				if existing, ok := mf.Concepts[match]; ok {
					seen := make(map[string]bool)
					for _, s := range existing.Sources {
						seen[s] = true
					}
					for _, s := range c.Sources {
						if !seen[s] {
							existing.Sources = append(existing.Sources, s)
						}
					}
					mf.Concepts[match] = existing
				}
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

	result.ConceptsExtracted = len(concepts)

	var conceptNames []string
	for _, c := range concepts {
		conceptNames = append(conceptNames, c.Name)
		mf.AddConcept(c.Name, filepath.Join(cfg.Output, "concepts", c.Name+".md"), c.Sources)
	}
	progress.ConceptsDiscovered(conceptNames)
	progress.EndPhase()
	client.TeardownCache(extCacheID)

	// Pass 3: Write articles
	if len(concepts) == 0 {
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

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	writeOntStore := opts.OntStore
	if writeOntStore == nil {
		writeOntStore = ontology.NewStore(opts.DB, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))
	}

	client.SetPass("write")
	var writeCacheID string
	if opts.CacheEnabled {
		writeCacheID, _ = client.SetupCache("You are a knowledge base article writer. Write comprehensive, well-structured wiki articles.", writeModel)
	}
	relPatterns := ontology.RelationPatterns(merged)
	progress.StartPhase("Pass 3: Write articles", len(concepts))
	articles := WriteArticles(ArticleWriteOpts{
		ProjectDir:       opts.ProjectDir,
		OutputDir:        cfg.Output,
		Client:           client,
		Model:            writeModel,
		MaxTokens:        articleMaxTokens,
		MaxParallel:      cfg.Compiler.MaxParallel,
		MemStore:         opts.MemStore,
		VecStore:         opts.VecStore,
		OntStore:         writeOntStore,
		ChunkStore:       opts.ChunkStore,
		DB:               opts.DB,
		Embedder:         opts.Embedder,
		UserTZ:           cfg.Compiler.UserTimeLocation(),
		ArticleFields:    cfg.Compiler.ArticleFields,
		RelationPatterns: relPatterns,
		ChunkSize:        cfg.Search.ChunkSizeOrDefault(),
		SplitThreshold:   cfg.Compiler.SplitThreshold,
		Language:         cfg.Language,
		Backpressure:     opts.Backpressure,
	}, concepts)

	for _, ar := range articles {
		if ar.Error != nil {
			result.Errors++
			progress.ItemError(ar.ConceptName, ar.Error)
		} else {
			result.ArticlesWritten++
			progress.ItemDone(ar.ConceptName, ar.ArticlePath)

			// Per-article quality scoring
			if opts.ItemStore != nil {
				articlePath := filepath.Join(opts.ProjectDir, ar.ArticlePath)
				articleContent, _ := os.ReadFile(articlePath)

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

				scores := ScoreArticle(string(articleContent), sourceText, ar.ConceptName, mf, writeOntStore)
				for _, srcPath := range mf.Concepts[ar.ConceptName].Sources {
					if err := opts.ItemStore.SetQualityScore(srcPath, scores.Combined); err != nil {
						log.Warn("set quality score failed", "path", srcPath, "error", err)
					}
				}

				if scores.Combined < 0.5 {
					log.Warn("low quality article", "concept", ar.ConceptName,
						"score", scores.Combined, "coverage", scores.SourceCoverage,
						"completeness", scores.ExtractionCompleteness, "crossref", scores.CrossRefDensity)
				}
			}
		}
	}
	progress.EndPhase()
	client.TeardownCache(writeCacheID)

	return result
}
