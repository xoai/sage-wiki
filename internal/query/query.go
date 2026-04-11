package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/graph"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// QueryResult holds the answer and metadata.
type QueryResult struct {
	Question    string
	Answer      string
	Sources     []string // article paths used
	Format      string   // markdown, terminal, marp
	OutputPath  string   // if auto-filed
}

// QueryOpts allows callers to pass shared resources.
type QueryOpts struct {
	DB *storage.DB // optional — opened from project dir if nil
}

// Query performs a Q&A operation: search → read articles → LLM synthesis.
func Query(projectDir string, question string, format string, topK int, opts ...QueryOpts) (*QueryResult, error) {
	if format == "" {
		format = "markdown"
	}
	if topK <= 0 {
		topK = 5
	}

	// Load config
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("query: load config: %w", err)
	}

	// Use shared DB or open one
	var db *storage.DB
	var closeDB bool
	if len(opts) > 0 && opts[0].DB != nil {
		db = opts[0].DB
	} else {
		db, err = storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
		if err != nil {
			return nil, fmt.Errorf("query: open db: %w", err)
		}
		closeDB = true
	}
	if closeDB {
		defer db.Close()
	}

	contextStr, sources, err := buildQueryContext(projectDir, question, topK, cfg, db)
	if err != nil {
		return nil, err
	}

	if contextStr == "" {
		return &QueryResult{
			Question: question,
			Answer:   "No relevant articles found in the wiki for this question.",
			Format:   format,
		}, nil
	}

	// Create LLM client
	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit)
	if err != nil {
		return nil, fmt.Errorf("query: create LLM client: %w", err)
	}

	model := cfg.Models.Query
	if model == "" {
		model = cfg.Models.Write
	}

	// Synthesize answer
	formatInstruction := ""
	switch format {
	case "marp":
		formatInstruction = "\nFormat the answer as Marp slides (use --- for slide breaks, add marp: true frontmatter)."
	case "terminal":
		formatInstruction = "\nFormat for terminal display: no images, concise, use bullet points."
	default:
		formatInstruction = "\nFormat as markdown with [[wikilinks]] for cross-references."
	}

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are a knowledge base Q&A assistant. Answer questions using the provided wiki articles as context. Cite sources using [[wikilinks]]. Be precise and factual."},
		{Role: "user", Content: fmt.Sprintf("Question: %s%s\n\n## Wiki Context:\n\n%s", question, formatInstruction, contextStr)},
	}, llm.CallOpts{Model: model, MaxTokens: 4000})
	if err != nil {
		return nil, fmt.Errorf("query: LLM synthesis: %w", err)
	}

	result := &QueryResult{
		Question: question,
		Answer:   resp.Content,
		Sources:  sources,
		Format:   format,
	}

	// Auto-file to outputs/
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(cfg.Ontology.Relations)))
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)
	outputPath, err := autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow(), autoFileOpts{ChunkStore: chunkStore, DB: db, ChunkSize: cfg.Search.ChunkSizeOrDefault()})
	if err != nil {
		log.Warn("auto-filing failed", "error", err)
	} else {
		result.OutputPath = outputPath
	}

	return result, nil
}

// buildQueryContext runs hybrid search + ontology traversal and assembles
// the article context string. Returns ("", nil, nil) if no results found.
func buildQueryContext(projectDir string, question string, topK int, cfg *config.Config, db *storage.DB) (string, []string, error) {
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(cfg.Ontology.Relations)))
	chunkStore := memory.NewChunkStore(db)
	embedder := embed.NewFromConfig(cfg)

	var graphExpanded []graphExpandedArticle

	// Try enhanced search if chunks are available
	chunkCount, _ := chunkStore.Count()
	if chunkCount > 0 {
		// Determine rerank eligibility — auto-disable for Ollama unless explicitly enabled
		rerankEnabled := cfg.Search.RerankEnabled()
		if cfg.API.Provider == "ollama" && cfg.Search.Rerank == nil {
			rerankEnabled = false
			log.Info("reranking disabled for local models — enable with search.rerank: true")
		}

		// Create LLM client for expansion/reranking (best-effort, nil = skip)
		var client *llm.Client
		if cfg.Search.QueryExpansionEnabled() || rerankEnabled {
			client, _ = llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit)
		}

		model := cfg.Models.Query
		if model == "" {
			model = cfg.Models.Write
		}

		enhanced, err := search.EnhancedSearch(search.EnhancedSearchOpts{
			Query:          question,
			Limit:          topK,
			Client:         client,
			Model:          model,
			Embedder:       embedder,
			ChunkStore:     chunkStore,
			MemStore:       memStore,
			VecStore:       vecStore,
			QueryExpansion: cfg.Search.QueryExpansionEnabled(),
			RerankEnabled:  rerankEnabled,
		})
		if err != nil {
			log.Warn("enhanced search failed, falling back to doc-level", "error", err)
		} else if len(enhanced) > 0 {
			// Compute graph expansion from enhanced search seeds
			if cfg.Search.GraphExpansionEnabled() {
				seedIDs := extractSeedIDsFromEnhanced(enhanced)
				graphExpanded = computeGraphExpansion(cfg, ontStore, seedIDs)
			}
			return buildContextFromEnhanced(projectDir, cfg.Output, enhanced, ontStore, graphExpanded, cfg.Search.ContextMaxTokensOrDefault())
		}
	} else if chunkCount == 0 {
		count, _ := memStore.Count()
		if count > 0 {
			log.Info("chunk index empty — using document-level search. Run `sage-wiki compile` to build chunk index.")
		}
	}

	// Fallback: document-level hybrid search
	return buildDocLevelContext(projectDir, question, topK, memStore, vecStore, ontStore, embedder, cfg, graphExpanded)
}

// buildContextFromEnhanced assembles article context from enhanced search results.
func buildContextFromEnhanced(projectDir string, outputDir string, results []search.SearchResult, ontStore *ontology.Store, graphExpanded []graphExpandedArticle, maxTokens int) (string, []string, error) {
	var ctx strings.Builder
	var sources []string
	seen := map[string]bool{}
	tokenBudget := maxTokens
	if tokenBudget <= 0 {
		tokenBudget = 8000
	}
	tokensUsed := 0
	maxPerArticle := 16000 // 4000 tokens * 4 chars/token

	for _, r := range results {
		docID := r.DocID
		articlePath := docIDToArticlePath(docID, outputDir)
		if articlePath == "" || seen[articlePath] {
			continue
		}
		absPath := filepath.Join(projectDir, articlePath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxPerArticle {
			content = content[:maxPerArticle]
		}
		contentTokens := len(content) / 4
		if tokensUsed+contentTokens > tokenBudget {
			break
		}
		seen[articlePath] = true
		ctx.WriteString(fmt.Sprintf("### Source: %s\n%s\n\n---\n\n", articlePath, content))
		sources = append(sources, articlePath)
		tokensUsed += contentTokens
	}

	// Graph-expanded articles (higher quality than depth-1 BFS)
	for _, ge := range graphExpanded {
		if ge.ArticlePath == "" || seen[ge.ArticlePath] {
			continue
		}
		absPath := filepath.Join(projectDir, ge.ArticlePath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxPerArticle {
			content = content[:maxPerArticle]
		}
		contentTokens := len(content) / 4
		if tokensUsed+contentTokens > tokenBudget {
			break
		}
		seen[ge.ArticlePath] = true
		ctx.WriteString(fmt.Sprintf("### Graph-related: %s\n%s\n\n---\n\n", ge.ArticlePath, content))
		sources = append(sources, ge.ArticlePath)
		tokensUsed += contentTokens
	}

	// Fallback: depth-1 ontology traversal for articles not yet seen
	for _, r := range results {
		entityID := r.DocID
		if len(entityID) > 8 && entityID[:8] == "concept:" {
			entityID = entityID[8:]
		}
		related, _ := ontStore.Traverse(entityID, ontology.TraverseOpts{
			Direction: ontology.Both,
			MaxDepth:  1,
		})
		for _, rel := range related {
			if rel.ArticlePath != "" && !seen[rel.ArticlePath] {
				absPath := filepath.Join(projectDir, rel.ArticlePath)
				if data, err := os.ReadFile(absPath); err == nil {
					content := string(data)
					if len(content) > maxPerArticle {
						content = content[:maxPerArticle]
					}
					contentTokens := len(content) / 4
					if tokensUsed+contentTokens > tokenBudget {
						break
					}
					seen[rel.ArticlePath] = true
					ctx.WriteString(fmt.Sprintf("### Related: %s\n%s\n\n---\n\n", rel.ArticlePath, content))
					tokensUsed += contentTokens
				}
			}
		}
	}

	return ctx.String(), sources, nil
}

// buildDocLevelContext is the original document-level search path.
func buildDocLevelContext(projectDir string, question string, topK int,
	memStore *memory.Store, vecStore *vectors.Store, ontStore *ontology.Store,
	embedder embed.Embedder, cfg *config.Config, graphExpanded []graphExpandedArticle) (string, []string, error) {

	searcher := hybrid.NewSearcher(memStore, vecStore)

	var queryVec []float32
	if embedder != nil {
		queryVec, _ = embedder.Embed(question)
	}

	results, err := searcher.Search(hybrid.SearchOpts{
		Query: question,
		Limit: topK,
	}, queryVec)
	if err != nil {
		return "", nil, fmt.Errorf("query: search: %w", err)
	}

	if len(results) == 0 {
		return "", nil, nil
	}

	// Compute graph expansion from doc-level search seeds if not already done
	if cfg.Search.GraphExpansionEnabled() && len(graphExpanded) == 0 {
		seedIDs := extractSeedIDsFromDocLevel(results)
		graphExpanded = computeGraphExpansion(cfg, ontStore, seedIDs)
	}

	tokenBudget := cfg.Search.ContextMaxTokensOrDefault()
	tokensUsed := 0
	maxPerArticle := 16000 // 4000 tokens * 4 chars/token

	var ctx strings.Builder
	var sources []string
	seen := map[string]bool{}

	for _, r := range results {
		if r.ArticlePath == "" || seen[r.ArticlePath] {
			continue
		}
		absPath := filepath.Join(projectDir, r.ArticlePath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxPerArticle {
			content = content[:maxPerArticle]
		}
		contentTokens := len(content) / 4
		if tokensUsed+contentTokens > tokenBudget {
			break
		}
		seen[r.ArticlePath] = true
		ctx.WriteString(fmt.Sprintf("### Source: %s\n%s\n\n---\n\n", r.ArticlePath, content))
		sources = append(sources, r.ArticlePath)
		tokensUsed += contentTokens
	}

	// Graph-expanded articles
	for _, ge := range graphExpanded {
		if ge.ArticlePath == "" || seen[ge.ArticlePath] {
			continue
		}
		absPath := filepath.Join(projectDir, ge.ArticlePath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxPerArticle {
			content = content[:maxPerArticle]
		}
		contentTokens := len(content) / 4
		if tokensUsed+contentTokens > tokenBudget {
			break
		}
		seen[ge.ArticlePath] = true
		ctx.WriteString(fmt.Sprintf("### Graph-related: %s\n%s\n\n---\n\n", ge.ArticlePath, content))
		sources = append(sources, ge.ArticlePath)
		tokensUsed += contentTokens
	}

	// Fallback: depth-1 ontology traversal
	for _, r := range results {
		if r.ID == "" {
			continue
		}
		entityID := r.ID
		if len(entityID) > 8 && entityID[:8] == "concept:" {
			entityID = entityID[8:]
		}
		related, _ := ontStore.Traverse(entityID, ontology.TraverseOpts{
			Direction: ontology.Both,
			MaxDepth:  1,
		})
		for _, rel := range related {
			if rel.ArticlePath != "" && !seen[rel.ArticlePath] {
				absPath := filepath.Join(projectDir, rel.ArticlePath)
				if data, err := os.ReadFile(absPath); err == nil {
					content := string(data)
					if len(content) > maxPerArticle {
						content = content[:maxPerArticle]
					}
					contentTokens := len(content) / 4
					if tokensUsed+contentTokens > tokenBudget {
						break
					}
					seen[rel.ArticlePath] = true
					ctx.WriteString(fmt.Sprintf("### Related: %s\n%s\n\n---\n\n", rel.ArticlePath, content))
					tokensUsed += contentTokens
				}
			}
		}
	}

	return ctx.String(), sources, nil
}

// docIDToArticlePath converts a doc ID like "concept:my-concept" to "{outputDir}/concepts/my-concept.md".
func docIDToArticlePath(docID string, outputDir string) string {
	if strings.HasPrefix(docID, "concept:") {
		name := docID[8:]
		return filepath.Join(outputDir, "concepts", name+".md")
	}
	if strings.HasPrefix(docID, "summary:") {
		name := docID[8:]
		return filepath.Join(outputDir, "summaries", name+".md")
	}
	if strings.HasPrefix(docID, "output:") {
		name := docID[7:]
		return filepath.Join(outputDir, "outputs", name)
	}
	return ""
}

// SaveAnswer saves a Q&A answer to the outputs/ directory with frontmatter,
// FTS5 indexing, embeddings, and ontology edges.
func SaveAnswer(projectDir string, question string, answer string, sources []string, db *storage.DB) (string, error) {
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return "", err
	}
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(cfg.Ontology.Relations)))
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)
	result := &QueryResult{
		Question: question,
		Answer:   answer,
		Sources:  sources,
		Format:   "markdown",
	}
	return autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow(), autoFileOpts{ChunkStore: chunkStore, DB: db, ChunkSize: cfg.Search.ChunkSizeOrDefault()})
}

// autoFileOpts holds optional stores for chunk indexing in autoFile.
type autoFileOpts struct {
	ChunkStore *memory.ChunkStore
	DB         *storage.DB
	ChunkSize  int // tokens per chunk (0 = default 800)
}

// autoFile saves the query result to wiki/outputs/ with frontmatter.
func autoFile(projectDir string, outputDir string, result *QueryResult,
	memStore *memory.Store, vecStore *vectors.Store, ontStore *ontology.Store,
	embedder embed.Embedder, userNow string, opts ...autoFileOpts) (string, error) {

	outputsDir := filepath.Join(projectDir, outputDir, "outputs")
	os.MkdirAll(outputsDir, 0755)

	timestamp := time.Now().Format("2006-01-02")
	slug := slugify(result.Question)
	filename := fmt.Sprintf("%s-%s.md", timestamp, slug)
	relPath := filepath.Join(outputDir, "outputs", filename)
	absPath := filepath.Join(projectDir, relPath)

	frontmatter := fmt.Sprintf(`---
question: "%s"
sources: [%s]
created_at: %s
format: %s
---

`, result.Question, strings.Join(result.Sources, ", "), userNow, result.Format)

	if err := os.WriteFile(absPath, []byte(frontmatter+result.Answer), 0644); err != nil {
		return "", err
	}

	// Index in FTS5
	memStore.Add(memory.Entry{
		ID:          "output:" + filename,
		Content:     result.Answer,
		Tags:        []string{"output"},
		ArticlePath: relPath,
	})

	// Embed
	if embedder != nil {
		if vec, err := embedder.Embed(result.Answer); err == nil {
			vecStore.Upsert("output:"+filename, vec)
		}
	}

	// Create ontology artifact + derived_from edges
	ontStore.AddEntity(ontology.Entity{
		ID:          "output:" + filename,
		Type:        ontology.TypeArtifact,
		Name:        result.Question,
		ArticlePath: relPath,
	})

	for _, src := range result.Sources {
		// Extract concept ID from path
		conceptID := strings.TrimSuffix(filepath.Base(src), ".md")
		ontStore.AddRelation(ontology.Relation{
			ID:       "output:" + filename + "-derived-" + conceptID,
			SourceID: "output:" + filename,
			TargetID: conceptID,
			Relation: ontology.RelDerivedFrom,
		})
	}

	// Chunk-index the output if ChunkStore is available
	if len(opts) > 0 && opts[0].ChunkStore != nil && opts[0].DB != nil {
		cs := opts[0].ChunkStore
		docID := "output:" + filename
		chunkSize := 800
		if opts[0].ChunkSize > 0 {
			chunkSize = opts[0].ChunkSize
		}
		chunks := extract.ChunkText(result.Answer, chunkSize)

		// Embed chunks outside transaction
		var chunkEmbeddings [][]float32
		if embedder != nil {
			chunkEmbeddings = make([][]float32, len(chunks))
			for i, c := range chunks {
				if vec, err := embedder.Embed(c.Text); err == nil {
					chunkEmbeddings[i] = vec
				}
			}
		}

		if err := opts[0].DB.WriteTx(func(tx *sql.Tx) error {
			if err := cs.DeleteDocChunks(tx, docID); err != nil {
				return err
			}
			entries := make([]memory.ChunkEntry, len(chunks))
			for i, c := range chunks {
				entries[i] = memory.ChunkEntry{
					ChunkID:    fmt.Sprintf("%s:c%d", docID, i),
					ChunkIndex: c.Index,
					Heading:    c.Heading,
					Content:    c.Text,
				}
			}
			if err := cs.IndexChunks(tx, docID, entries); err != nil {
				return err
			}
			if chunkEmbeddings != nil {
				for i, emb := range chunkEmbeddings {
					if emb != nil {
						vecStore.UpsertChunk(tx, entries[i].ChunkID, docID, emb)
					}
				}
			}
			return nil
		}); err != nil {
			log.Warn("chunk indexing failed for output", "path", relPath, "error", err)
		}
	}

	log.Info("query result filed", "path", relPath)
	return relPath, nil
}

// StreamQuery performs Q&A with streaming token output and auto-files to outputs/.
// The context is used to cancel the LLM call on client disconnect.
func StreamQuery(ctx context.Context, projectDir string, question string, topK int, tokenCB func(string), db *storage.DB) ([]string, error) {
	if topK <= 0 {
		topK = 5
	}

	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("query: load config: %w", err)
	}

	var closeDB bool
	if db == nil {
		db, err = storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
		if err != nil {
			return nil, fmt.Errorf("query: open db: %w", err)
		}
		closeDB = true
	}
	if closeDB {
		defer db.Close()
	}

	contextStr, sources, err := buildQueryContext(projectDir, question, topK, cfg, db)
	if err != nil {
		return nil, err
	}

	if contextStr == "" {
		tokenCB("No relevant articles found in the wiki for this question.")
		return nil, nil
	}

	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit)
	if err != nil {
		return nil, fmt.Errorf("query: create LLM client: %w", err)
	}

	model := cfg.Models.Query
	if model == "" {
		model = cfg.Models.Write
	}

	messages := []llm.Message{
		{Role: "system", Content: "You are a knowledge base Q&A assistant. Answer questions using the provided wiki articles as context. Cite sources using [[wikilinks]]. Be precise and factual.\nFormat as markdown with [[wikilinks]] for cross-references."},
		{Role: "user", Content: fmt.Sprintf("Question: %s\n\n## Wiki Context:\n\n%s", question, contextStr)},
	}

	resp, err := client.ChatCompletionStream(ctx, messages, llm.CallOpts{Model: model, MaxTokens: 4000}, tokenCB)
	if err != nil {
		return sources, fmt.Errorf("query: LLM stream: %w", err)
	}

	// Auto-file the result to outputs/
	if resp != nil && resp.Content != "" {
		result := &QueryResult{
			Question: question,
			Answer:   resp.Content,
			Sources:  sources,
			Format:   "markdown",
		}
		memStore := memory.NewStore(db)
		vecStore := vectors.NewStore(db)
		ontStore := ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(cfg.Ontology.Relations)))
		embedder := embed.NewFromConfig(cfg)
		chunkStore := memory.NewChunkStore(db)
		if outputPath, err := autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow(), autoFileOpts{ChunkStore: chunkStore, DB: db, ChunkSize: cfg.Search.ChunkSizeOrDefault()}); err != nil {
			log.Warn("stream auto-filing failed", "error", err)
		} else {
			log.Info("stream query result filed", "path", outputPath)
		}
	}

	return sources, nil
}

// graphExpandedArticle represents an article discovered via graph expansion.
type graphExpandedArticle struct {
	EntityID    string
	ArticlePath string
	Score       float64
}

// computeGraphExpansion runs the graph relevance scorer and returns expanded articles.
// Returns nil if no seeds, expansion disabled, or on error.
func computeGraphExpansion(cfg *config.Config, ontStore *ontology.Store, seedIDs []string) []graphExpandedArticle {
	if len(seedIDs) == 0 {
		return nil
	}

	scored, err := graph.ScoreRelevance(ontStore, graph.RelevanceOpts{
		SeedIDs:   seedIDs,
		MaxExpand: cfg.Search.GraphMaxExpandOrDefault(),
		MaxDepth:  cfg.Search.GraphDepthOrDefault(),
		Weights: graph.RelevanceWeights{
			DirectLink:     cfg.Search.WeightDirectLinkOrDefault(),
			SourceOverlap:  cfg.Search.WeightSourceOverlapOrDefault(),
			CommonNeighbor: cfg.Search.WeightCommonNeighborOrDefault(),
			TypeAffinity:   cfg.Search.WeightTypeAffinityOrDefault(),
		},
	})
	if err != nil {
		log.Debug("graph expansion failed", "error", err)
		return nil
	}

	var expanded []graphExpandedArticle
	for _, s := range scored {
		e, err := ontStore.GetEntity(s.EntityID)
		if err != nil || e == nil || e.ArticlePath == "" {
			continue
		}
		expanded = append(expanded, graphExpandedArticle{
			EntityID:    s.EntityID,
			ArticlePath: e.ArticlePath,
			Score:       s.Score,
		})
	}
	if len(expanded) > 0 {
		log.Debug("graph expansion added articles", "count", len(expanded))
	}
	return expanded
}

// extractSeedIDsFromEnhanced extracts entity IDs from enhanced search results.
func extractSeedIDsFromEnhanced(results []search.SearchResult) []string {
	var ids []string
	seen := map[string]bool{}
	for _, r := range results {
		id := r.DocID
		if strings.HasPrefix(id, "concept:") {
			id = id[8:]
		} else if strings.HasPrefix(id, "summary:") {
			continue
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

// extractSeedIDsFromDocLevel extracts entity IDs from hybrid search results.
func extractSeedIDsFromDocLevel(results []hybrid.SearchResult) []string {
	var ids []string
	seen := map[string]bool{}
	for _, r := range results {
		id := r.ID
		if strings.HasPrefix(id, "concept:") {
			id = id[8:]
		}
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' {
			result.WriteRune('-')
		}
	}
	slug := result.String()
	if len(slug) > 50 {
		slug = slug[:50]
	}
	return strings.Trim(slug, "-")
}
