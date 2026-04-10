package query

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
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
	outputPath, err := autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow())
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
	searcher := hybrid.NewSearcher(memStore, vecStore)

	embedder := embed.NewFromConfig(cfg)
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
		seen[r.ArticlePath] = true
		ctx.WriteString(fmt.Sprintf("### Source: %s\n%s\n\n---\n\n", r.ArticlePath, string(data)))
		sources = append(sources, r.ArticlePath)
	}

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
					seen[rel.ArticlePath] = true
					ctx.WriteString(fmt.Sprintf("### Related: %s\n%s\n\n---\n\n", rel.ArticlePath, string(data)))
				}
			}
		}
	}

	return ctx.String(), sources, nil
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
	result := &QueryResult{
		Question: question,
		Answer:   answer,
		Sources:  sources,
		Format:   "markdown",
	}
	return autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow())
}

// autoFile saves the query result to wiki/outputs/ with frontmatter.
func autoFile(projectDir string, outputDir string, result *QueryResult,
	memStore *memory.Store, vecStore *vectors.Store, ontStore *ontology.Store,
	embedder embed.Embedder, userNow string) (string, error) {

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
		if outputPath, err := autoFile(projectDir, cfg.Output, result, memStore, vecStore, ontStore, embedder, cfg.Compiler.UserNow()); err != nil {
			log.Warn("stream auto-filing failed", "error", err)
		} else {
			log.Info("stream query result filed", "path", outputPath)
		}
	}

	return sources, nil
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
