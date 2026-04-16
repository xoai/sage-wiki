package compiler

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// ReExtract re-runs Pass 2 (concept extraction) and Pass 3 (article writing)
// using existing summaries from wiki/summaries/. Skips Pass 0 and Pass 1.
func ReExtract(projectDir string) (*CompileResult, error) {
	result := &CompileResult{}

	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: load config: %w", err)
	}

	// Load user prompt overrides if prompts/ directory exists
	if err := prompts.LoadFromDir(filepath.Join(projectDir, "prompts")); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}

	mf, err := manifest.Load(filepath.Join(projectDir, ".manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: load manifest: %w", err)
	}

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
			SummaryPath: filepath.Join(cfg.Output, "summaries", e.Name()),
			Summary:     string(data),
		})
	}

	log.Info("re-extract: loaded existing summaries", "count", len(summaries))

	if len(summaries) == 0 {
		return result, fmt.Errorf("re-extract: no summaries found — run sage-wiki compile first")
	}

	// Create LLM client
	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit, cfg.API.ExtraParams)
	if err != nil {
		return nil, fmt.Errorf("re-extract: create LLM client: %w", err)
	}

	// Open DB
	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return nil, fmt.Errorf("re-extract: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes))
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)

	// Pass 2: Concept extraction
	extractModel := cfg.Models.Extract
	if extractModel == "" {
		extractModel = cfg.Models.Summarize
	}

	log.Info("Pass 2: extracting concepts", "from_summaries", len(summaries))
	concepts, err := ExtractConcepts(summaries, mf.Concepts, client, extractModel)
	if err != nil {
		return nil, fmt.Errorf("re-extract: concept extraction: %w", err)
	}
	result.ConceptsExtracted = len(concepts)

	// Update manifest with concepts
	for _, c := range concepts {
		mf.AddConcept(c.Name, filepath.Join(cfg.Output, "concepts", c.Name+".md"), c.Sources)
	}

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
			} else {
				result.ArticlesWritten++
			}
		}
	}

	// Save manifest
	if err := mf.Save(filepath.Join(projectDir, ".manifest.json")); err != nil {
		return nil, fmt.Errorf("re-extract: save manifest: %w", err)
	}

	log.Info("re-extract complete", "concepts", result.ConceptsExtracted, "articles", result.ArticlesWritten, "errors", result.Errors)
	return result, nil
}
