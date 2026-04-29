package compiler

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

var (
	blockSplitRe = regexp.MustCompile(`\n\n|\n#{1,3}\s`)
	wikilinkRe   = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

// ArticleResult holds the output of writing a concept article.
type ArticleResult struct {
	ConceptName string
	ArticlePath string
	Error       error
}

// ArticleWriteOpts bundles all parameters for WriteArticles / writeOneArticle.
type ArticleWriteOpts struct {
	ProjectDir       string
	OutputDir        string
	Client           *llm.Client
	Model            string
	MaxTokens        int
	MaxParallel      int
	MemStore         *memory.Store
	VecStore         *vectors.Store
	OntStore         *ontology.Store
	ChunkStore       *memory.ChunkStore
	DB               *storage.DB
	Embedder         embed.Embedder
	UserTZ           *time.Location
	ArticleFields    []string
	RelationPatterns []ontology.RelationPattern
	ChunkSize        int // tokens per chunk (default 800)
	SplitThreshold   int // chars — enable section-aware writing above this (default 15000)
	Language         string
	Backpressure     *BackpressureController // optional; if nil, uses fixed semaphore
}

// WriteArticles runs Pass 3: write concept articles with ontology edges.
func WriteArticles(opts ArticleWriteOpts, concepts []ExtractedConcept) []ArticleResult {
	maxParallel := opts.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 20
	}

	results := make([]ArticleResult, len(concepts))
	var wg sync.WaitGroup
	var done atomic.Int32
	total := len(concepts)

	// Use BackpressureController if available, otherwise fixed semaphore
	var sem chan struct{}
	if opts.Backpressure == nil {
		sem = make(chan struct{}, maxParallel)
	}

	for i, concept := range concepts {
		wg.Add(1)

		var release func()
		if opts.Backpressure != nil {
			release = opts.Backpressure.Acquire()
		} else {
			sem <- struct{}{}
			release = func() { <-sem }
		}

		go func(idx int, c ExtractedConcept) {
			defer wg.Done()
			defer release()

			result := writeOneArticle(opts, c)
			results[idx] = result

			n := int(done.Add(1))
			if result.Error != nil {
				if opts.Backpressure != nil && llm.IsRateLimitError(result.Error) {
					delay := opts.Backpressure.OnRateLimit()
					log.Warn("rate limited in write pass, backing off", "delay", delay, "new_limit", opts.Backpressure.CurrentLimit())
					time.Sleep(delay)
				}
				log.Error("write article failed", "progress", fmt.Sprintf("%d/%d", n, total), "concept", c.Name, "error", result.Error)
			} else {
				if opts.Backpressure != nil {
					opts.Backpressure.OnSuccess()
				}
				log.Info("article written", "progress", fmt.Sprintf("%d/%d", n, total), "concept", c.Name)
			}
		}(i, concept)
	}

	wg.Wait()
	return results
}

func writeOneArticle(opts ArticleWriteOpts, concept ExtractedConcept) ArticleResult {
	result := ArticleResult{ConceptName: concept.Name}

	// Check for existing article
	articlePath := filepath.Join(opts.OutputDir, "concepts", concept.Name+".md")
	absPath := filepath.Join(opts.ProjectDir, articlePath)
	var existingContent string
	if data, err := os.ReadFile(absPath); err == nil {
		existingContent = string(data)
	}

	// Build source context from relevant sections (document splitting)
	sourceContext := buildSourceContext(opts.ProjectDir, concept, opts.SplitThreshold)

	// Build prompt
	relatedNames := findRelatedConcepts(concept)
	prompt, err := prompts.Render("write_article", prompts.WriteArticleData{
		ConceptName:     formatConceptName(concept.Name),
		ConceptID:       concept.Name,
		Sources:         strings.Join(concept.Sources, ", "),
		RelatedConcepts: relatedNames,
		ExistingArticle: existingContent,
		Aliases:         strings.Join(concept.Aliases, ", "),
		SourceList:      strings.Join(concept.Sources, ", "),
		RelatedList:     strings.Join(relatedNames, ", "),
		Confidence:      "medium",
		MaxTokens:       opts.MaxTokens,
		SourceContext:    sourceContext,
	}, opts.Language)
	if err != nil {
		result.Error = fmt.Errorf("render write_article prompt: %w", err)
		return result
	}

	resp, err := opts.Client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are a wiki author writing comprehensive, precise articles for a personal knowledge base. Use [[wikilinks]] for cross-references. Do not include YAML frontmatter."},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: opts.Model, MaxTokens: opts.MaxTokens})
	if err != nil {
		result.Error = fmt.Errorf("llm call: %w", err)
		return result
	}

	articleContent := resp.Content

	// Strip any LLM-generated frontmatter — code builds frontmatter from ground-truth data.
	articleContent = stripLLMFrontmatter(articleContent)

	// Extract LLM-judged fields (confidence + any custom fields from config)
	fields, articleContent := extractFields(articleContent, opts.ArticleFields)

	// Build frontmatter: ground-truth fields + LLM-judged fields
	articleContent = buildFrontmatter(concept, fields, opts.ArticleFields, opts.UserTZ) + "\n\n" + articleContent

	// Note: wikilinks are kept even if targets don't exist yet.
	// Future compiles will create the missing articles, and the links
	// will resolve naturally. Broken links are surfaced by `sage-wiki lint`.

	// Write article file
	articleDir := filepath.Join(opts.ProjectDir, opts.OutputDir, "concepts")
	os.MkdirAll(articleDir, 0755)

	if err := os.WriteFile(absPath, []byte(articleContent), 0644); err != nil {
		result.Error = fmt.Errorf("write file: %w", err)
		return result
	}
	result.ArticlePath = articlePath

	// Create ontology entity — pass through LLM-assigned type if valid,
	// fall back to concept for unknown or empty types.
	entityType := concept.Type
	if entityType == "" || !opts.OntStore.IsValidType(entityType) {
		entityType = ontology.TypeConcept
	}

	if err := opts.OntStore.AddEntity(ontology.Entity{
		ID:          concept.Name,
		Type:        entityType,
		Name:        formatConceptName(concept.Name),
		ArticlePath: articlePath,
	}); err != nil {
		log.Error("failed to create ontology entity", "concept", concept.Name, "error", err)
	}

	// Create source citation relations
	for _, src := range concept.Sources {
		// Create source entity if not exists
		if err := opts.OntStore.AddEntity(ontology.Entity{
			ID:   src,
			Type: ontology.TypeSource,
			Name: filepath.Base(src),
		}); err != nil {
			log.Warn("failed to create source entity", "source", src, "error", err)
		}
		if err := opts.OntStore.AddRelation(ontology.Relation{
			ID:       concept.Name + "-cites-" + sanitizeID(src),
			SourceID: concept.Name,
			TargetID: src,
			Relation: ontology.RelCites,
		}); err != nil {
			log.Warn("failed to create cites relation", "concept", concept.Name, "source", src, "error", err)
		}
	}

	// Extract typed relations from article text
	extractRelations(concept.Name, articleContent, opts.OntStore, opts.RelationPatterns)

	// Index in FTS5
	if err := opts.MemStore.Add(memory.Entry{
		ID:          "concept:" + concept.Name,
		Content:     articleContent,
		Tags:        append([]string{entityType}, concept.Aliases...),
		ArticlePath: articlePath,
	}); err != nil {
		log.Error("failed to index article", "concept", concept.Name, "error", err)
	}

	// Generate embedding
	if opts.Embedder != nil {
		vec, err := opts.Embedder.Embed(articleContent)
		if err != nil {
			log.Warn("embedding failed for article", "concept", concept.Name, "error", err)
		} else {
			opts.VecStore.Upsert("concept:"+concept.Name, vec)
		}
	}

	// Index chunks for enhanced search
	if opts.ChunkStore != nil && opts.DB != nil {
		chunkSize := opts.ChunkSize
		if chunkSize <= 0 {
			chunkSize = 800
		}
		docID := "concept:" + concept.Name
		chunks := extract.ChunkText(articleContent, chunkSize)

		// Embed all chunks FIRST (API calls outside transaction)
		var chunkEmbeddings [][]float32
		if opts.Embedder != nil {
			chunkEmbeddings = make([][]float32, len(chunks))
			for i, c := range chunks {
				vec, err := opts.Embedder.Embed(c.Text)
				if err != nil {
					log.Warn("chunk embedding failed", "concept", concept.Name, "chunk", i, "error", err)
				} else {
					chunkEmbeddings[i] = vec
				}
			}
		}

		// Single WriteTx: delete old + insert new
		if err := opts.DB.WriteTx(func(tx *sql.Tx) error {
			if err := opts.ChunkStore.DeleteDocChunks(tx, docID); err != nil {
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

			if err := opts.ChunkStore.IndexChunks(tx, docID, entries); err != nil {
				return err
			}

			// Insert pre-computed chunk embeddings
			if chunkEmbeddings != nil {
				for i, emb := range chunkEmbeddings {
					if emb != nil {
						if err := opts.VecStore.UpsertChunk(tx, entries[i].ChunkID, docID, emb); err != nil {
							log.Warn("chunk vector upsert failed", "chunk", entries[i].ChunkID, "error", err)
						}
					}
				}
			}

			return nil
		}); err != nil {
			log.Error("chunk indexing failed", "concept", concept.Name, "error", err)
		}
	}

	return result
}

func buildFrontmatter(concept ExtractedConcept, fields map[string]string, fieldOrder []string, loc *time.Location) string {
	aliases := quoteYAMLList(concept.Aliases)
	sources := quoteYAMLList(concept.Sources)

	confidence := fields["confidence"]
	if confidence == "" {
		confidence = "medium"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\nconcept: %s\naliases: %s\nsources: %s\nconfidence: %s",
		concept.Name, aliases, sources, confidence)

	// Append custom fields in declared order (deterministic)
	for _, k := range fieldOrder {
		if v := fields[k]; v != "" {
			fmt.Fprintf(&b, "\n%s: %s", k, v)
		}
	}

	fmt.Fprintf(&b, "\ncreated_at: %s\n---", timeNow(loc))
	return b.String()
}

// extractFields scans the tail of the LLM response for "Key: value" lines matching
// the given field names, removes them from the body, and returns a map of extracted values.
// Only the last 15 lines are scanned to avoid false positives in article body text.
// "confidence" is always extracted and normalized via mapConfidence.
// LLMs may format keys with bold markdown (**Key:** or **Key**:), which is handled.
func extractFields(content string, fieldNames []string) (fields map[string]string, cleaned string) {
	// Build lookup set: always include "confidence"
	want := map[string]bool{"confidence": true}
	for _, f := range fieldNames {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}

	fields = make(map[string]string)
	lines := strings.Split(content, "\n")

	// Only scan the last 15 lines to avoid false positives in article body
	scanStart := 0
	if len(lines) > 15 {
		scanStart = len(lines) - 15
	}

	var kept []string
	kept = append(kept, lines[:scanStart]...)

	for _, line := range lines[scanStart:] {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// Strip bold/backtick markdown: **Key:** value, **Key**: value, `Key`: value
		stripped := strings.TrimLeft(lower, "*`")
		stripped = strings.TrimSpace(stripped)

		matched := false
		for name := range want {
			// Match "name:" or "name**:" or "name`:" patterns
			prefix := name + ":"
			altPrefix := name + "**:"
			if strings.HasPrefix(stripped, prefix) || strings.HasPrefix(stripped, altPrefix) {
				// Extract value after the colon
				colonIdx := strings.Index(lower, ":")
				if colonIdx >= 0 {
					value := strings.TrimSpace(trimmed[colonIdx+1:])
					value = strings.Trim(value, "*` ")
					if name == "confidence" {
						value = mapConfidence(value)
					}
					fields[name] = value
				}
				matched = true
				break
			}
		}

		if !matched {
			kept = append(kept, line)
		}
	}

	// Default confidence if not found
	if _, ok := fields["confidence"]; !ok {
		fields["confidence"] = "medium"
	}

	return fields, strings.TrimSpace(strings.Join(kept, "\n"))
}

// stripLLMFrontmatter removes any frontmatter block the LLM may have generated.
// Handles bare (---\n...\n---) and code-fenced (```yaml\n---\n...\n---\n```) formats.
func stripLLMFrontmatter(content string) string {
	s := strings.TrimSpace(content)

	// Case 1: code-fenced frontmatter — ```yaml\n---\n...\n---\n```
	if strings.HasPrefix(s, "```") {
		// Find the closing fence
		firstNewline := strings.Index(s, "\n")
		if firstNewline < 0 {
			return s
		}
		rest := s[firstNewline+1:]
		closeFence := strings.Index(rest, "```")
		if closeFence >= 0 {
			s = strings.TrimSpace(rest[closeFence+3:])
			// The inner block may itself be bare frontmatter — fall through
		}
	}

	// Case 2: bare frontmatter — ---\n...\n---
	if strings.HasPrefix(s, "---") {
		// Find the closing ---
		after := s[3:]
		if idx := strings.Index(after, "\n---"); idx >= 0 {
			s = strings.TrimSpace(after[idx+4:])
		}
	}

	return s
}

// quoteYAMLList produces a YAML list with properly quoted values.
func quoteYAMLList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func formatConceptName(name string) string {
	words := strings.Split(name, "-")
	for i, w := range words {
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func findRelatedConcepts(concept ExtractedConcept) []string {
	// Related concepts are discovered during extraction as co-occurrences
	// For now, return empty — the ontology will be populated as articles are written
	return nil
}

// extractRelations parses article text for relationship patterns and creates ontology edges.
// Splits article into semantic blocks (paragraph breaks and headings) and only creates
// relations when a keyword co-occurs with a [[wikilink]] in the same block.
func extractRelations(conceptID string, content string, ontStore *ontology.Store, patterns []ontology.RelationPattern) {
	blocks := blockSplitRe.Split(content, -1)

	for _, block := range blocks {
		blockLower := strings.ToLower(block)
		links := wikilinkRe.FindAllStringSubmatch(block, -1)

		for _, m := range links {
			target := m[1]
			if target == conceptID {
				continue
			}

			for _, rp := range patterns {
				for _, keyword := range rp.Keywords {
					if strings.Contains(blockLower, keyword) {
						ontStore.AddRelation(ontology.Relation{
							ID:       conceptID + "-" + rp.Relation + "-" + target,
							SourceID: conceptID,
							TargetID: target,
							Relation: rp.Relation,
						})
						break // one relation type per target is enough
					}
				}
			}
		}
	}
}

func sanitizeID(s string) string {
	return strings.NewReplacer("/", "-", "\\", "-", ".", "-", " ", "-").Replace(s)
}

func mapConfidence(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch {
	case v == "high" || v == "5" || v == "5/5" || v == "100%" || v == "certain" || v == "very high":
		return "high"
	case v == "medium" || v == "3" || v == "4" || v == "3/5" || v == "4/5" || v == "moderate" || v == "60%" || v == "70%" || v == "80%":
		return "medium"
	case v == "low" || v == "1" || v == "2" || v == "1/5" || v == "2/5" || v == "uncertain" || v == "speculative":
		return "low"
	default:
		return "medium" // default to medium for unknown values
	}
}

// validateWikilinks removes [[links]] that point to non-existent concept articles.
func validateWikilinks(projectDir, outputDir, content string) string {
	conceptsDir := filepath.Join(projectDir, outputDir, "concepts")

	re := regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		target := match[2 : len(match)-2] // strip [[ and ]]

		// Check if article exists
		articlePath := filepath.Join(conceptsDir, target+".md")
		if _, err := os.Stat(articlePath); err == nil {
			return match // valid link, keep it
		}

		// Link is broken — return just the text without brackets
		return target
	})
}

// buildSourceContext reads source files for a concept, splits large ones
// by headings, and returns the relevant sections as context for article writing.
// For small sources (below threshold), includes the full content.
// Returns empty string if no sources can be read.
func buildSourceContext(projectDir string, concept ExtractedConcept, threshold int) string {
	if threshold <= 0 {
		threshold = 15000 // default from spec
	}

	var parts []string
	terms := append([]string{concept.Name, formatConceptName(concept.Name)}, concept.Aliases...)

	for _, srcPath := range concept.Sources {
		absPath := filepath.Join(projectDir, srcPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)

		sections := extract.SplitByHeadings(content, threshold)
		if len(sections) <= 1 {
			// Small doc or no headings — include as-is (truncated)
			if len(content) > 4000 {
				content = content[:4000] + "\n[...truncated...]"
			}
			parts = append(parts, fmt.Sprintf("### Source: %s\n\n%s", srcPath, content))
			continue
		}

		// Large doc — select relevant sections only
		relevant := extract.SectionsContaining(sections, terms)
		if len(relevant) == 0 {
			// No sections match — use first section as fallback
			if len(sections) > 0 {
				relevant = sections[:1]
			}
		}

		for _, s := range relevant {
			header := srcPath
			if s.Heading != "" {
				header = srcPath + " > " + s.Heading
			}
			text := s.Content
			if len(text) > 4000 {
				text = text[:4000] + "\n[...truncated...]"
			}
			parts = append(parts, fmt.Sprintf("### Source: %s\n\n%s", header, text))
		}
	}

	return strings.Join(parts, "\n\n---\n\n")
}
