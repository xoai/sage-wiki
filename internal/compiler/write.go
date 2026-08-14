package compiler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/fsutil"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/store"
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
	ProjectDir             string
	OutputDir              string
	Client                 *llm.Client
	Model                  string
	MaxTokens              int
	MaxParallel            int
	MemStore               store.EntryStore
	VecStore               store.VectorStore
	OntStore               store.OntologyStore
	ChunkStore             store.ChunkStore
	DB                     store.DBHandle
	Embedder               embed.Embedder
	UserTZ                 *time.Location
	ArticleFields          []string
	RelationPatterns       []ontology.RelationPattern
	ChunkSize              int // tokens per chunk (default 800)
	ChunkOverlap           int // tokens of overlap between adjacent chunks (default 0)
	SplitThreshold         int // chars — enable section-aware writing above this (default 15000)
	MaxSourceContextTokens int // max estimated tokens in assembled source context (default 100000)
	Language               string
	Backpressure           *BackpressureController // optional; if nil, uses fixed semaphore
	Prompts                *prompts.Registry       // optional; nil = prompts package default
	AntiPatternPhrases     []string                // sentences containing these are stripped (issue #95); nil/empty → no strip
	// AllConcepts is the FULL manifest concept set (Name + Sources), used to
	// seed each article's "See also" [[wikilinks]] from co-occurring concepts
	// and to canonicalize display-form links to concepts outside the current
	// batch. Nil → no related-concept seeding (backward compatible). Issue #106.
	AllConcepts []ExtractedConcept
	// Ctx carries compile cancellation; nil = background. When cancelled, the
	// write loop stops launching new articles and in-flight LLM calls abort.
	Ctx context.Context
	// Temperature (SPEC-04 D2): the compile sampling temperature.
	Temperature *float64
}

// writeApplyHookForTest is the fault-injection seam for the
// cancel-during-apply witness (SPEC-04 D3, spec test 6). Called with each
// item's input index immediately BEFORE that item's apply step. Nil in
// production; test-only, same precedent as ShuffleSourcesForTest.
var writeApplyHookForTest func(idx int)

// WriteArticles runs Pass 3: write concept articles with ontology edges.
//
// SPEC-04 D3 (deferred application): the LLM fan-out only PREPARES payloads
// (prompt, article bytes, embeddings, chunk rows) — every store mutation and
// file write happens in a sequential post-join apply loop in INPUT order, so
// SQLite rowid order (and everything derived from it: SWVI bytes, FTS
// layout) is independent of goroutine completion order.
func WriteArticles(opts ArticleWriteOpts, concepts []ExtractedConcept) []ArticleResult {
	defer metrics.ObserveDuration(metrics.HistogramNamed("compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "write"), time.Now())
	maxParallel := opts.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 20
	}

	results := make([]ArticleResult, len(concepts))
	prepared := make([]*articlePayload, len(concepts))
	var wg sync.WaitGroup
	var done atomic.Int32
	total := len(concepts)

	// Build the alias→concept-id map once for wikilink sanitization (issue #95).
	// The in-batch concept slice is the authoritative alias source; AllConcepts
	// (the full manifest set) additionally lets display-form links to concepts
	// outside this batch canonicalize instead of being stripped (issue #106).
	aliasMap := buildAliasMap(concepts, opts.AllConcepts)

	// Build the co-occurrence index once (source-sharing concepts), then look
	// it up per article to seed the "See also" [[wikilinks]]. Read-only after
	// construction, so it is safe to share across the write goroutines below.
	// Sourced from the full manifest set so incremental compiles link to
	// pre-existing concepts too (issue #106).
	relatedIndex := buildRelatedConceptsIndex(opts.AllConcepts, maxRelatedConcepts)

	// Use BackpressureController if available, otherwise fixed semaphore
	var sem chan struct{}
	if opts.Backpressure == nil {
		sem = make(chan struct{}, maxParallel)
	}

	for i, concept := range concepts {
		// Stop launching new article writes once the compile is cancelled;
		// already-launched writes finish or abort via their ctx-bound LLM call.
		if opts.Ctx != nil && opts.Ctx.Err() != nil {
			break
		}
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

			payload, result := prepareArticle(opts, c, aliasMap, relatedIndex[c.Name])
			prepared[idx] = payload
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
				log.Info("article prepared", "progress", fmt.Sprintf("%d/%d", n, total), "concept", c.Name)
			}
		}(i, concept)
	}

	wg.Wait()

	// Apply phase: sequential, input order. A cancel observed here stops the
	// loop — the run is incomplete, so the caller's final manifest/checkpoint
	// save is skipped (P1-1: an incomplete run persists no new CLAIMS; rows
	// already applied are content-identical on the next run's rewrite).
	for i, payload := range prepared {
		if payload == nil {
			continue // prepare failed — results[i] already carries the error
		}
		if writeApplyHookForTest != nil {
			writeApplyHookForTest(i)
		}
		if opts.Ctx != nil && opts.Ctx.Err() != nil {
			log.Warn("write pass: cancel observed during apply — remaining articles deferred to next compile", "applied", i, "total", len(prepared))
			break
		}
		applyArticlePayload(opts, payload, &results[i])
	}

	return results
}

// articlePayload is everything the apply phase needs, computed in the
// concurrent phase without touching any store (SPEC-04 D3).
type articlePayload struct {
	concept         ExtractedConcept
	articlePath     string
	absPath         string
	content         string
	entityType      string
	conceptVec      []float32 // nil when the concept embed failed (logged in prepare)
	chunks          []extract.Chunk
	chunkEmbeddings [][]float32
}

func writeOneArticle(opts ArticleWriteOpts, concept ExtractedConcept, aliasMap map[string]string, relatedNames []string) ArticleResult {
	payload, result := prepareArticle(opts, concept, aliasMap, relatedNames)
	if payload != nil {
		applyArticlePayload(opts, payload, &result)
	}
	return result
}

// prepareArticle does the LLM call and all pure computation for one
// article. It performs NO store mutations and NO file writes.
func prepareArticle(opts ArticleWriteOpts, concept ExtractedConcept, aliasMap map[string]string, relatedNames []string) (*articlePayload, ArticleResult) {
	result := ArticleResult{ConceptName: concept.Name}

	// Check for existing article
	articlePath := filepath.ToSlash(filepath.Join(opts.OutputDir, "concepts", concept.Name+".md"))
	absPath := filepath.Join(opts.ProjectDir, articlePath)
	var existingContent string
	if data, err := os.ReadFile(absPath); err == nil {
		existingContent = string(data)
	}

	// Build source context from relevant sections (document splitting)
	sourceContext := buildSourceContext(opts.ProjectDir, concept, opts.SplitThreshold, opts.MaxSourceContextTokens)

	// Build prompt. relatedNames are real, co-occurring concept slugs (issue
	// #106) that resolve to existing article files and survive the strip pass.
	prompt, err := renderPrompt(opts.Prompts, "write_article", prompts.WriteArticleData{
		ConceptName:     ontology.FormatConceptName(concept.Name),
		ConceptID:       concept.Name,
		Sources:         strings.Join(concept.Sources, ", "),
		RelatedConcepts: relatedNames,
		ExistingArticle: existingContent,
		Aliases:         strings.Join(concept.Aliases, ", "),
		SourceList:      strings.Join(concept.Sources, ", "),
		RelatedList:     strings.Join(relatedNames, ", "),
		Confidence:      "medium",
		MaxTokens:       opts.MaxTokens,
		SourceContext:   sourceContext,
	}, opts.Language)
	if err != nil {
		result.Error = fmt.Errorf("render write_article prompt: %w", err)
		return nil, result
	}

	resp, err := opts.Client.ChatCompletionCtx(opts.Ctx, []llm.Message{
		{Role: "system", Content: "You are a wiki author writing comprehensive, precise articles for a personal knowledge base. Use [[wikilinks]] for cross-references. Do not include YAML frontmatter."},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: opts.Model, MaxTokens: opts.MaxTokens, Temperature: opts.Temperature})
	if err != nil {
		result.Error = fmt.Errorf("llm call: %w", err)
		return nil, result
	}

	// Guard before writing: an empty/reasoning-truncated response must fail the
	// concept (retried next compile) rather than write a hollow article.
	if gErr := emptyContentError(resp, "article", concept.Name); gErr != nil {
		result.Error = gErr
		return nil, result
	}

	articleContent := resp.Content

	// Strip an outer code fence some LLMs wrap the whole response in — run first
	// so the inner frontmatter becomes detectable below (issue #95).
	articleContent = stripOuterCodeFence(articleContent)

	// Strip any LLM-generated frontmatter — code builds frontmatter from ground-truth data.
	articleContent = stripLLMFrontmatter(articleContent)

	// Extract LLM-judged fields (confidence + any custom fields from config)
	fields, articleContent := extractFields(articleContent, opts.ArticleFields)

	// Resolve ontology entity type — pass through LLM-assigned type if valid,
	// fall back to concept for unknown or empty types. This resolved type is
	// emitted into the article frontmatter AND used for ontology entity creation.
	entityType := concept.Type
	if entityType == "" || !opts.OntStore.IsValidType(entityType) {
		entityType = ontology.TypeConcept
	}

	// Post-process the article BODY before frontmatter is prepended (issue #95).
	// Running these on the body (not the assembled doc) guarantees the YAML
	// frontmatter — which contains source paths with periods — is never touched.
	articleContent = stripAntiPatternSentences(articleContent, opts.AntiPatternPhrases)
	articleContent = sanitizeWikilinks(articleContent, aliasMap)

	// Build frontmatter: ground-truth fields + LLM-judged fields
	articleContent = buildFrontmatter(concept, entityType, fields, opts.ArticleFields, opts.UserTZ) + "\n\n" + articleContent

	payload := &articlePayload{
		concept:     concept,
		articlePath: articlePath,
		absPath:     absPath,
		content:     articleContent,
		entityType:  entityType,
	}

	// Concept embedding (network call — concurrent phase)
	if opts.Embedder != nil {
		vec, err := opts.Embedder.Embed(articleContent)
		if err != nil {
			log.Warn("embedding failed for article", "concept", concept.Name, "error", err)
		} else {
			payload.conceptVec = vec
		}
	}

	// Chunk text + chunk embeddings (network calls — concurrent phase)
	if opts.ChunkStore != nil && opts.DB != nil {
		chunkSize := opts.ChunkSize
		if chunkSize <= 0 {
			chunkSize = 800
		}
		payload.chunks = extract.ChunkText(articleContent, chunkSize, opts.ChunkOverlap)
		if opts.Embedder != nil {
			payload.chunkEmbeddings = make([][]float32, len(payload.chunks))
			for i, c := range payload.chunks {
				vec, err := opts.Embedder.Embed(c.Text)
				if err != nil {
					log.Warn("chunk embedding failed", "concept", concept.Name, "chunk", i, "error", err)
				} else {
					payload.chunkEmbeddings[i] = vec
				}
			}
		}
	}

	return payload, result
}

// applyArticlePayload performs every store mutation and the file write for
// one prepared article. Called sequentially in input order from WriteArticles
// (SPEC-04 D3). Error semantics preserved from the pre-D3 code: a file-write
// failure fails the article; store-mutation failures log and continue.
func applyArticlePayload(opts ArticleWriteOpts, payload *articlePayload, result *ArticleResult) {
	concept := payload.concept
	articleContent := payload.content
	articlePath := payload.articlePath
	absPath := payload.absPath
	entityType := payload.entityType

	// Canonical write-then-index order (I2): (1) write the article file
	// atomically, (2) index into the DB (ontology + FTS + vectors, below),
	// (3) the manifest is marked once at the end of the compile via MergeSave.
	articleDir := filepath.Join(opts.ProjectDir, opts.OutputDir, "concepts")
	os.MkdirAll(articleDir, 0755)

	if err := fsutil.WriteFileAtomic(absPath, []byte(articleContent), 0644); err != nil {
		result.Error = fmt.Errorf("write file: %w", err)
		return
	}
	result.ArticlePath = articlePath

	if err := opts.OntStore.AddEntity(ontology.Entity{
		ID:          concept.Name,
		Type:        entityType,
		Name:        ontology.FormatConceptName(concept.Name),
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

	// Origin date: a compiled article is as fresh as its newest evidence —
	// the max source_date over its contributing sources (ADR-039; no
	// contributing date ⇒ no row, never a compile timestamp).
	srcIDs := make([]string, len(concept.Sources))
	for i, s := range concept.Sources {
		srcIDs[i] = "src:" + s
	}
	if dates, err := opts.MemStore.GetSourceDates(srcIDs); err != nil {
		log.Warn("source dates unavailable for article", "concept", concept.Name, "error", err)
	} else if ts := sourcedate.Max(dates, srcIDs); ts > 0 {
		if err := opts.MemStore.SetSourceDate("concept:"+concept.Name, ts); err != nil {
			log.Warn("article source date not recorded", "concept", concept.Name, "error", err)
		}
	}

	// Concept vector (computed in prepare)
	if payload.conceptVec != nil {
		if err := opts.VecStore.Upsert("concept:"+concept.Name, payload.conceptVec); err != nil {
			log.Warn("concept vector upsert failed", "concept", concept.Name, "error", err)
		}
	}

	// Index chunks for enhanced search
	if opts.ChunkStore != nil && opts.DB != nil {
		docID := "concept:" + concept.Name
		chunks := payload.chunks
		chunkEmbeddings := payload.chunkEmbeddings

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
		} else {
			opts.VecStore.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit
		}
	}
}

func buildFrontmatter(concept ExtractedConcept, entityType string, fields map[string]string, fieldOrder []string, loc *time.Location) string {
	// SPEC-04 D1 (plan-review F-035): aliases/sources are SETS serialized as
	// lists — emit them sorted so frontmatter bytes are canonical regardless
	// of LLM/merge slice order. The manifest's internal order is unchanged
	// (LLM-derived content order, like prose).
	aliases := quoteYAMLList(sortedCopy(concept.Aliases))
	sources := quoteYAMLList(sortedCopy(concept.Sources))

	confidence := fields["confidence"]
	if confidence == "" {
		confidence = "medium"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\nconcept: %s\nentity_type: %s\naliases: %s\nsources: %s\nconfidence: %s",
		concept.Name, entityType, aliases, sources, confidence)

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

// stripOuterCodeFence removes a triple-backtick fence that wraps the ENTIRE
// article body — some LLMs (GLM/Qwen) emit ```markdown ... ``` around their
// whole response. It strips ONLY when the trimmed content starts with a fence,
// ends with a fence, AND contains exactly two fence markers; an article that
// merely contains a code block (fence not at position 0) or has multiple code
// blocks (>2 fences) is left untouched, so real code is never corrupted.
// Issue #95.
func stripOuterCodeFence(content string) string {
	s := strings.TrimSpace(content)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") {
		return content
	}
	if strings.Count(s, "```") != 2 {
		return content
	}
	// Drop the opening fence line (consumes any ```lang info string).
	nl := strings.Index(s, "\n")
	if nl < 0 {
		return content // single-line like ```x``` — nothing to unwrap
	}
	body := s[nl+1:]
	// Drop the closing fence (the last ``` in the remaining body).
	closeIdx := strings.LastIndex(body, "```")
	if closeIdx < 0 {
		return content
	}
	return strings.TrimSpace(body[:closeIdx])
}

// stripAntiPatternSentences drops sentences containing any forbidden filler/meta
// phrase. nil or empty phrases → identity (the config accessor resolves the
// default before this is reached). Lines inside ``` fenced regions are left
// intact. Sentences split on EN (.!?) and 中文 (。！？) terminators; matching is
// case-insensitive substring. Never empties the article: if the result is
// blank, the original input is returned. Issue #95.
func stripAntiPatternSentences(content string, phrases []string) string {
	if len(phrases) == 0 {
		return content
	}
	lowered := make([]string, len(phrases))
	for i, p := range phrases {
		lowered[i] = strings.ToLower(p)
	}

	lines := strings.Split(content, "\n")
	inFence := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		out = append(out, filterAntiPatternSentences(line, lowered))
	}

	result := strings.Join(out, "\n")
	if strings.TrimSpace(result) == "" {
		return content // guard: never empty the whole article
	}
	return result
}

// filterAntiPatternSentences splits one line into sentences (keeping their
// terminators) and drops any sentence that contains a lowercased phrase.
func filterAntiPatternSentences(line string, loweredPhrases []string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	runes := []rune(line)
	var b strings.Builder
	start := 0

	flush := func(end int) {
		seg := string(runes[start:end])
		start = end
		low := strings.ToLower(seg)
		for _, p := range loweredPhrases {
			if strings.Contains(low, p) {
				return // drop this sentence
			}
		}
		b.WriteString(seg)
	}

	for i, r := range runes {
		switch r {
		case '.', '!', '?', '。', '！', '？':
			flush(i + 1)
		}
	}
	if start < len(runes) {
		flush(len(runes))
	}
	return b.String()
}

// sanitizeWikilinks rewrites [[alias]] (or [[alias|display]]) to the canonical
// concept id when alias resolves in aliasMap. The link target is the text
// before any pipe; the display part (if any) is preserved. Unresolved links
// pass through unchanged. nil/empty map → identity. Issue #95.
func sanitizeWikilinks(content string, aliasMap map[string]string) string {
	if len(aliasMap) == 0 {
		return content
	}
	return wikilinkRe.ReplaceAllStringFunc(content, func(match string) string {
		inner := match[2 : len(match)-2] // strip [[ and ]]
		target := inner
		display := ""
		if i := strings.Index(inner, "|"); i >= 0 {
			target = inner[:i]
			display = inner[i:] // keep leading "|" + display text
		}
		if mapped, ok := aliasMap[target]; ok && mapped != target {
			return "[[" + mapped + display + "]]"
		}
		return match
	})
}

// buildAliasMap builds an alias→concept-id lookup for wikilink sanitization.
// Aliases come only from the in-batch concept slice (the authoritative alias
// source at compile time — the ontology store and manifest hold no aliases).
// Canonical ids and display forms are added for every concept in allConcepts
// (the full manifest set, a superset of the batch) so display-form links to
// concepts OUTSIDE the current batch also canonicalize and survive the strip
// pass rather than being dropped (issue #106). When allConcepts is nil it
// falls back to the batch, preserving the original issue-#95 behavior.
func buildAliasMap(concepts []ExtractedConcept, allConcepts []ExtractedConcept) map[string]string {
	m := make(map[string]string)
	// Manifest aliases first (#128: manifest.Concept carries them since the
	// evidence-gate work) — an out-of-batch alias link like [[RAP]] can then
	// canonicalize to remedial-action-plan. In-batch aliases override.
	for _, c := range allConcepts {
		for _, a := range c.Aliases {
			if a != "" {
				m[a] = c.Name
			}
		}
	}
	for _, c := range concepts {
		for _, a := range c.Aliases {
			if a != "" {
				m[a] = c.Name
			}
		}
	}
	// Add canonical ids and display forms AFTER aliases so a real concept's
	// own id/name always wins over a colliding alias from another concept
	// (e.g. concept B named "attention" must beat concept A's alias "attention").
	canonicalSet := allConcepts
	if len(canonicalSet) == 0 {
		canonicalSet = concepts
	}
	for _, c := range canonicalSet {
		m[c.Name] = c.Name
		m[ontology.FormatConceptName(c.Name)] = c.Name
	}
	return m
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

// sortedCopy returns an ascending-sorted copy of the input (SPEC-04 D1).
func sortedCopy(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

// maxRelatedConcepts caps how many "See also" links each article is seeded
// with. The cap is by design: a source cited by many concepts would otherwise
// flood every article with links. Ranking by shared-source count keeps the
// strongest co-occurrences; a richer signal (vector similarity) is a follow-up.
const maxRelatedConcepts = 8

// buildRelatedConceptsIndex builds, for every concept, the list of other
// concepts it co-occurs with — i.e. concepts that cite at least one common
// source document. Co-occurrence is the mechanism the original stub intended
// ("discovered during extraction as co-occurrences"); it needs no embeddings,
// works on a cold compile, and yields real slugs that resolve to article files
// and survive the strip pass (issue #106).
//
// Results per concept are ranked by shared-source count (desc), tie-broken by
// name (asc) for determinism despite Go map iteration order, then truncated to
// cap. Returns conceptName → []relatedSlug. A nil/empty input yields an empty
// map (so behavior matches the old stub when AllConcepts is unset).
//
// Cost is ~O(sources × concepts-per-source); a pathological source cited by
// every concept is O(N²). The cap bounds output size, not build cost — fine
// for typical wikis.
func buildRelatedConceptsIndex(all []ExtractedConcept, limit int) map[string][]string {
	index := make(map[string][]string, len(all))
	if len(all) == 0 {
		return index
	}

	// source → set of concept names citing it. Sets dedupe repeated sources
	// within a concept's list (the manifest stores Sources verbatim) so a
	// duplicate cannot inflate co-occurrence counts.
	bySource := make(map[string]map[string]bool)
	for _, c := range all {
		seen := make(map[string]bool, len(c.Sources))
		for _, s := range c.Sources {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			if bySource[s] == nil {
				bySource[s] = make(map[string]bool)
			}
			bySource[s][c.Name] = true
		}
	}

	for _, c := range all {
		// Count shared sources with each co-occurring concept (exclude self).
		shared := make(map[string]int)
		seen := make(map[string]bool, len(c.Sources))
		for _, s := range c.Sources {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			for other := range bySource[s] {
				if other != c.Name {
					shared[other]++
				}
			}
		}
		if len(shared) == 0 {
			continue
		}

		related := make([]string, 0, len(shared))
		for name := range shared {
			related = append(related, name)
		}
		sort.Slice(related, func(i, j int) bool {
			if shared[related[i]] != shared[related[j]] {
				return shared[related[i]] > shared[related[j]] // more shared sources first
			}
			return related[i] < related[j] // deterministic tie-break
		})
		if len(related) > limit {
			related = related[:limit]
		}
		index[c.Name] = related
	}

	return index
}

// extractRelations parses article text for relationship patterns and creates ontology edges.
// Splits article into semantic blocks (paragraph breaks and headings) and only creates
// relations when a keyword co-occurs with a [[wikilink]] in the same block.
func extractRelations(conceptID string, content string, ontStore store.OntologyStore, patterns []ontology.RelationPattern) {
	blocks := blockSplitRe.Split(content, -1)

	sourceEntity, err := ontStore.GetEntity(conceptID)
	sourceType := ""
	sourceKnown := err == nil
	if sourceEntity != nil {
		sourceType = sourceEntity.Type
	}

	for _, block := range blocks {
		blockLower := strings.ToLower(block)
		links := wikilinkRe.FindAllStringSubmatch(block, -1)

		for _, m := range links {
			target := m[1]
			if target == conceptID {
				continue
			}

			targetEntity, err := ontStore.GetEntity(target)
			targetType := ""
			targetKnown := err == nil
			if targetEntity != nil {
				targetType = targetEntity.Type
			}

			for _, rp := range patterns {
				if sourceKnown && len(rp.ValidSources) > 0 && !typeInList(sourceType, rp.ValidSources) {
					continue
				}
				if targetKnown && len(rp.ValidTargets) > 0 && !typeInList(targetType, rp.ValidTargets) {
					continue
				}

				for _, keyword := range rp.Keywords {
					if strings.Contains(blockLower, keyword) {
						ontStore.AddRelation(ontology.Relation{
							ID:       conceptID + "-" + rp.Relation + "-" + target,
							SourceID: conceptID,
							TargetID: target,
							Relation: rp.Relation,
						})
						break
					}
				}
			}
		}
	}
}

func typeInList(t string, list []string) bool {
	for _, v := range list {
		if v == t {
			return true
		}
	}
	return false
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

// StripBrokenWikilinkStats summarizes one StripBrokenWikilinks sweep.
type StripBrokenWikilinkStats struct {
	ArticlesScanned int
	ArticlesEdited  int
	LinksStripped   int
}

// MaybeStripBrokenWikilinks runs the post-Pass-3 wikilink sweep when the
// config flag is enabled and logs the result. Use from every code path that
// finalizes article writing so the strip doesn't get lost (issue #94).
//
// The helper is a wrapper around StripBrokenWikilinks; callers that want
// custom logging or to act on the stats can call that directly.
func MaybeStripBrokenWikilinks(projectDir, outputDir string, enabled bool, memStore store.EntryStore) {
	if !enabled {
		return
	}
	stats, err := StripBrokenWikilinks(projectDir, outputDir, memStore)
	if err != nil {
		log.Warn("strip-broken-links failed", "error", err)
		return
	}
	if stats.LinksStripped > 0 {
		log.Info("stripped broken wikilinks",
			"links_stripped", stats.LinksStripped,
			"articles_edited", stats.ArticlesEdited,
			"articles_scanned", stats.ArticlesScanned)
	}
}

// StripBrokenWikilinks scans every article under <outputDir>/concepts and
// rewrites those that contain [[wikilinks]] to non-existent concept files,
// replacing the dead link with bare text. Intended to run once after Pass 3
// completes, when the on-disk set of concept articles is authoritative.
// Issue #90.
//
// When memStore is non-nil, each rewritten article's FTS entry is updated to the
// stripped content, keeping file and index consistent (write-then-index, I2) —
// otherwise the post-Pass-3 rewrite would leave FTS holding the pre-strip text,
// which the startup reconciler would later "heal" by re-embedding the article.
// The article filename equals the concept name, so the FTS id is
// "concept:"+<filename>. Chunk vectors are intentionally left as-is: removing a
// dead [[wikilink]] barely changes the article's semantics, so a re-embed is not
// worth its cost.
func StripBrokenWikilinks(projectDir, outputDir string, memStore store.EntryStore) (StripBrokenWikilinkStats, error) {
	var stats StripBrokenWikilinkStats
	conceptsDir := filepath.Join(projectDir, outputDir, "concepts")

	// Build the set of existing concept article slugs (filename without .md).
	entries, err := os.ReadDir(conceptsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, fmt.Errorf("strip-broken-links: read concepts dir: %w", err)
	}
	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		existing[strings.TrimSuffix(name, ".md")] = true
	}

	re := regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		articlePath := filepath.Join(conceptsDir, e.Name())
		data, err := os.ReadFile(articlePath)
		if err != nil {
			continue
		}
		stats.ArticlesScanned++

		stripped := 0
		rewritten := re.ReplaceAllStringFunc(string(data), func(match string) string {
			target := match[2 : len(match)-2]
			if existing[target] {
				return match
			}
			stripped++
			return target
		})
		if stripped == 0 {
			continue
		}
		if err := fsutil.WriteFileAtomic(articlePath, []byte(rewritten), 0644); err != nil {
			return stats, fmt.Errorf("strip-broken-links: write %s: %w", e.Name(), err)
		}
		// Keep FTS consistent with the rewritten file (I2). The article filename
		// (without .md) is the concept name, so its FTS id is "concept:"+name.
		if memStore != nil {
			id := "concept:" + strings.TrimSuffix(e.Name(), ".md")
			existing, gerr := memStore.Get(id)
			switch {
			case gerr != nil:
				// The reconciler heals the resulting content mismatch next startup.
				log.Warn("strip-broken-links: FTS lookup failed", "id", id, "error", gerr)
			case existing != nil:
				existing.Content = rewritten
				if uerr := memStore.Update(*existing); uerr != nil {
					log.Warn("strip-broken-links: FTS re-index failed", "id", id, "error", uerr)
				}
			}
		}
		stats.ArticlesEdited++
		stats.LinksStripped += stripped
	}
	return stats, nil
}

// buildSourceContext reads source files for a concept, splits large ones
// by headings, and returns the relevant sections as context for article writing.
// For small sources (below threshold), includes the full content.
// Returns empty string if no sources can be read.
type sourcePart struct {
	text          string
	matchCount    int
	sourcePath    string
	sectionOffset int
}

func buildSourceContext(projectDir string, concept ExtractedConcept, threshold int, maxTokens int) string {
	if threshold <= 0 {
		threshold = 15000 // default from spec
	}
	if maxTokens <= 0 {
		maxTokens = 100000
	}

	terms := append([]string{concept.Name, ontology.FormatConceptName(concept.Name)}, concept.Aliases...)

	var parts []sourcePart

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
			parts = append(parts, sourcePart{
				text:          fmt.Sprintf("### Source: %s\n\n%s", srcPath, content),
				matchCount:    1,
				sourcePath:    srcPath,
				sectionOffset: 0,
			})
			continue
		}

		// Large doc — select relevant sections only
		relevant := extract.SectionsContaining(sections, terms)
		matchCount := len(relevant)
		if len(relevant) == 0 {
			// No sections match — use first section as fallback
			if len(sections) > 0 {
				relevant = sections[:1]
			}
			matchCount = 0
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
			parts = append(parts, sourcePart{
				text:          fmt.Sprintf("### Source: %s\n\n%s", header, text),
				matchCount:    matchCount,
				sourcePath:    srcPath,
				sectionOffset: s.StartOffset,
			})
		}
	}

	// Sort by relevance (match count desc), then source path, then section offset.
	// Stable sort ensures determinism.
	sort.SliceStable(parts, func(i, j int) bool {
		if parts[i].matchCount != parts[j].matchCount {
			return parts[i].matchCount > parts[j].matchCount
		}
		if parts[i].sourcePath != parts[j].sourcePath {
			return parts[i].sourcePath < parts[j].sourcePath
		}
		return parts[i].sectionOffset < parts[j].sectionOffset
	})

	// Accumulate within the token budget.
	var kept []string
	totalTokens := 0
	dropped := 0
	for i, p := range parts {
		partTokens := extract.EstimateTokens(p.text)
		if i == 0 {
			// Always keep the highest-ranked part, even if it exceeds budget.
			kept = append(kept, p.text)
			totalTokens = partTokens
			continue
		}
		if totalTokens+partTokens > maxTokens {
			dropped = len(parts) - i
			break
		}
		kept = append(kept, p.text)
		totalTokens += partTokens
	}

	if dropped > 0 {
		log.Warn("source context budget exceeded; dropping lower-relevance sources",
			"concept", concept.Name,
			"kept", len(kept), "dropped", dropped,
			"budget_tokens", maxTokens,
			"estimated_tokens", totalTokens)
	}

	// Neutralize literal delimiter tags in the raw source text so a hostile
	// document can't close the write_article template's untrusted frame
	// early (SEC-04, site 5 — the template wraps this whole string).
	return prompts.NeutralizeTags(strings.Join(kept, "\n\n---\n\n"))
}
