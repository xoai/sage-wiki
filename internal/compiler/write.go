package compiler

import (
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
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/quality"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// Package-level compiled regexps (avoid recompilation in goroutines).
var (
	reWikilink       = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	reOuterCodeFence = regexp.MustCompile("(?s)^```(?:markdown|md)?\\s*\n(.*?)\\s*```\\s*$")
)

// ArticleResult holds the output of writing a concept article.
type ArticleResult struct {
	ConceptName  string
	ArticlePath  string
	Error        error
	QualityScore *quality.ArticleScore
	NeedsRework  bool
}

// WriteArticles runs Pass 3: write concept articles with ontology edges.
func WriteArticles(
	projectDir string,
	outputDir string,
	concepts []ExtractedConcept,
	client *llm.Client,
	model string,
	maxTokens int,
	maxParallel int,
	memStore *memory.Store,
	vecStore *vectors.Store,
	ontStore *ontology.Store,
	embedder embed.Embedder,
	userTZ *time.Location,
	articleFields []string,
	relationPatterns []ontology.RelationPattern,
	language string,
) []ArticleResult {
	if maxParallel <= 0 {
		maxParallel = 4
	}

	results := make([]ArticleResult, len(concepts))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var done atomic.Int32
	total := len(concepts)

	for i, concept := range concepts {
		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, c ExtractedConcept) {
			defer wg.Done()
			defer func() { <-sem }()

			result := writeOneArticle(projectDir, outputDir, c, client, model, maxTokens, memStore, vecStore, ontStore, embedder, userTZ, articleFields, relationPatterns, language)
			results[idx] = result

			n := int(done.Add(1))
			if result.Error != nil {
				log.Error("write article failed", "progress", fmt.Sprintf("%d/%d", n, total), "concept", c.Name, "error", result.Error)
			} else {
				log.Info("article written", "progress", fmt.Sprintf("%d/%d", n, total), "concept", c.Name)
			}
		}(i, concept)
	}

	wg.Wait()
	return results
}

func writeOneArticle(
	projectDir string,
	outputDir string,
	concept ExtractedConcept,
	client *llm.Client,
	model string,
	maxTokens int,
	memStore *memory.Store,
	vecStore *vectors.Store,
	ontStore *ontology.Store,
	embedder embed.Embedder,
	userTZ *time.Location,
	articleFields []string,
	relationPatterns []ontology.RelationPattern,
	language string,
) ArticleResult {
	result := ArticleResult{ConceptName: concept.Name}

	// Check for existing article
	articlePath := filepath.Join(outputDir, "concepts", concept.Name+".md")
	absPath := filepath.Join(projectDir, articlePath)
	var existingContent string
	if data, err := os.ReadFile(absPath); err == nil {
		existingContent = string(data)
	}

	// Load summary content for each source to ground the article in real data
	sourceContext := loadSourceSummaries(projectDir, outputDir, concept.Sources)

	// Build prompt
	relatedNames := findRelatedConcepts(concept)
	prompt := buildArticlePrompt(concept, existingContent, relatedNames, language, sourceContext)

	systemPrompt := `You are a wiki author writing articles for a personal knowledge base. Your articles must be GROUNDED in the source summaries provided — do NOT add generic textbook knowledge that is not supported by the sources.

Rules:
1. ONLY describe what the source documents actually contain. If a source describes a specific project implementation, describe THAT implementation, not the general concept from Wikipedia.
2. When the sources contain SQL, formulas, code, directory structures, or data tables — QUOTE THEM DIRECTLY in your article using code blocks or markdown tables. Do not paraphrase "the system uses SQL expressions" when you can show the actual SQL.
3. Use tables to present structured information (field lists, category breakdowns, comparison data). Prefer tables over bullet lists for multi-attribute data.
4. Use [[wikilinks]] in lowercase-hyphenated format ONLY. Never use Chinese characters in wikilinks (use [[module-boundary]] not [[模块边界]]).
5. Output raw Markdown only. Do NOT wrap output in code fences. Do NOT include YAML frontmatter.
6. Start directly with the first section heading.
7. For 变体/Variants section: list ONLY variations explicitly named in the sources. If none exist, state one plausible alternative approach NOT taken, with a brief reason why.
8. For 权衡/Trade-offs section: analyze from these angles — pick the 2-3 most applicable: (a) rigidity vs flexibility, (b) coupling vs isolation, (c) standardization cost vs consistency benefit, (d) operational overhead. Write at least 2 sentences per point. NEVER write "源文档未提及", "未详细说明", or "no explicit mention".`
	if language != "" {
		systemPrompt += fmt.Sprintf("\n9. Write ALL content (including section titles) in %s.", languageDisplayName(language))
	}

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		result.Error = fmt.Errorf("llm call: %w", err)
		return result
	}

	articleContent := resp.Content

	// Strip outer code fence if LLM wrapped output in ```markdown ... ```
	articleContent = stripOuterCodeFence(articleContent)

	// Strip any LLM-generated frontmatter — code builds frontmatter from ground-truth data.
	articleContent = stripLLMFrontmatter(articleContent)

	// Extract LLM-judged fields (confidence + any custom fields from config)
	fields, articleContent := extractFields(articleContent, articleFields)

	// Detect empty articles (LLM returned only whitespace or frontmatter-only)
	if len(strings.TrimSpace(articleContent)) < 50 {
		log.Warn("article body too short, possible LLM failure", "concept", concept.Name, "bodyLen", len(strings.TrimSpace(articleContent)))
		result.Error = fmt.Errorf("article body empty or too short (%d chars)", len(strings.TrimSpace(articleContent)))
		return result
	}

	// Build frontmatter: ground-truth fields + LLM-judged fields
	articleContent = buildFrontmatter(concept, fields, articleFields, userTZ) + "\n\n" + articleContent

	// Normalize confidence values to enum (high/medium/low)
	articleContent = normalizeConfidence(articleContent)

	// Sanitize Chinese wikilinks → english kebab-case
	knownConcepts := buildConceptAliasMap(ontStore, concept)
	articleContent = sanitizeWikilinks(articleContent, knownConcepts)

	// Strip anti-pattern sentences (LLM ignoring prompt rules)
	articleContent = stripAntiPatternSentences(articleContent)

	// Note: wikilinks are kept even if targets don't exist yet.
	// Future compiles will create the missing articles, and the links
	// will resolve naturally. Broken links are surfaced by `sage-wiki lint`.

	// Write article file
	articleDir := filepath.Join(projectDir, outputDir, "concepts")
	os.MkdirAll(articleDir, 0755)

	if err := os.WriteFile(absPath, []byte(articleContent), 0644); err != nil {
		result.Error = fmt.Errorf("write file: %w", err)
		return result
	}
	result.ArticlePath = articlePath

	// Create ontology entity
	entityType := ontology.TypeConcept
	if concept.Type == "technique" {
		entityType = ontology.TypeTechnique
	} else if concept.Type == "claim" {
		entityType = ontology.TypeClaim
	}

	if err := ontStore.AddEntity(ontology.Entity{
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
		if err := ontStore.AddEntity(ontology.Entity{
			ID:   src,
			Type: ontology.TypeSource,
			Name: filepath.Base(src),
		}); err != nil {
			log.Warn("failed to create source entity", "source", src, "error", err)
		}
		if err := ontStore.AddRelation(ontology.Relation{
			ID:       concept.Name + "-cites-" + sanitizeID(src),
			SourceID: concept.Name,
			TargetID: src,
			Relation: ontology.RelCites,
		}); err != nil {
			log.Warn("failed to create cites relation", "concept", concept.Name, "source", src, "error", err)
		}
	}

	// Extract typed relations from article text
	extractRelations(concept.Name, articleContent, ontStore, relationPatterns)

	// Index in FTS5
	if err := memStore.Add(memory.Entry{
		ID:          "concept:" + concept.Name,
		Content:     articleContent,
		Tags:        append([]string{entityType}, concept.Aliases...),
		ArticlePath: articlePath,
	}); err != nil {
		log.Error("failed to index article", "concept", concept.Name, "error", err)
	}

	// Generate embedding
	if embedder != nil {
		vec, err := embedder.Embed(articleContent)
		if err != nil {
			log.Warn("embedding failed for article", "concept", concept.Name, "error", err)
		} else {
			vecStore.Upsert("concept:"+concept.Name, vec)
		}
	}

	// Quality scoring (zero LLM calls)
	qs := quality.DefaultScorer().ScoreArticle(concept.Name, articleContent, sourceContext)
	result.QualityScore = &qs
	if qs.Composite < 0.4 {
		result.NeedsRework = true
		log.Warn("low quality article", "concept", concept.Name, "composite", fmt.Sprintf("%.2f", qs.Composite), "antiPatterns", qs.AntiPatterns)
	} else if qs.Composite < 0.6 {
		log.Info("moderate quality article", "concept", concept.Name, "composite", fmt.Sprintf("%.2f", qs.Composite))
	}

	return result
}

func buildArticlePrompt(concept ExtractedConcept, existing string, related []string, language string, sourceContext string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Write a wiki article about: %s\n", formatConceptName(concept.Name)))
	b.WriteString(fmt.Sprintf("Concept ID: %s\n", concept.Name))

	if len(concept.Aliases) > 0 {
		b.WriteString(fmt.Sprintf("Also known as: %s\n", strings.Join(concept.Aliases, ", ")))
	}

	if len(related) > 0 {
		b.WriteString(fmt.Sprintf("Related concepts: %s\n", strings.Join(related, ", ")))
	}

	// Inject source summaries — this is the key context that prevents hallucination
	if sourceContext != "" {
		b.WriteString("\n--- SOURCE SUMMARIES (base your article ONLY on this content) ---\n")
		b.WriteString(sourceContext)
		b.WriteString("\n--- END SOURCE SUMMARIES ---\n")
	}

	// Conditional few-shot: when sources contain code, show good vs bad example
	if sourceHasCode(sourceContext) {
		b.WriteString(`
QUALITY EXAMPLE — follow this pattern:

BAD (do not write like this):
"满期赔付率在 CTE 中预计算，使用 exposure_days 字段。"

GOOD (write like this instead):
满期赔付率使用以下 SQL 计算：
` + "```sql\n" + `exposure_days = LEAST(GREATEST(DATEDIFF(day, start_date, end_date), 0), 365)
earned_claim_ratio = SUM(claim_amount) / NULLIF(SUM(premium * exposure_days / 365.0), 0)
` + "```\n" + `
When sources contain SQL, formulas, config, or tables — reproduce them verbatim in code blocks or markdown tables. Never describe structure when you can show it.

`)
	}

	if existing != "" {
		b.WriteString("\n## Existing article (update/expand):\n")
		b.WriteString(existing)
		b.WriteString("\n")
	}

	// Section titles: use language-appropriate headings
	sectionDef := "## Definition"
	sectionHow := "## How it works"
	sectionVariants := "## Variants"
	sectionTradeoffs := "## Trade-offs"
	sectionSeeAlso := "## See also"
	if language == "zh" || language == "zh-cn" || language == "chinese" {
		sectionDef = "## 定义"
		sectionHow = "## 工作原理"
		sectionVariants = "## 变体"
		sectionTradeoffs = "## 权衡"
		sectionSeeAlso = "## 另见"
	}

	b.WriteString(fmt.Sprintf(`
Write the article with these sections:
1. %s — what this concept means in the context of the source documents
2. %s — how it is actually implemented (cite specifics from the sources)
3. %s — variations mentioned in the sources, if any
4. %s — limitations or trade-offs noted in the sources
5. %s — [[wikilinks]] to related concepts

CRITICAL: Base your article strictly on the source summaries above. Do NOT add generic textbook knowledge, industry examples, or tools/frameworks not mentioned in the sources. If the sources describe a project-specific implementation, describe THAT implementation.

Do NOT include YAML frontmatter.
Do NOT wrap output in code fences.
Start directly with %s.

Wikilink rules:
- Use [[concept-name]] format (lowercase-hyphenated)
- Link concepts that deserve standalone articles
- Do NOT link generic terms or math notation
`, sectionDef, sectionHow, sectionVariants, sectionTradeoffs, sectionSeeAlso, sectionDef))

	return b.String()
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
// Looks for patterns like "X implements Y", "X extends Y", etc. near [[wikilinks]].
func extractRelations(conceptID string, content string, ontStore *ontology.Store, patterns []ontology.RelationPattern) {
	linkRe := regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	links := linkRe.FindAllStringSubmatch(content, -1)

	// Collect unique linked concepts
	linkedConcepts := map[string]bool{}
	for _, m := range links {
		target := m[1]
		if target != conceptID {
			linkedConcepts[target] = true
		}
	}

	contentLower := strings.ToLower(content)

	for target := range linkedConcepts {
		targetLower := strings.ToLower(target)
		for _, rp := range patterns {
			for _, keyword := range rp.Keywords {
				// Look for the keyword near the concept mention
				if strings.Contains(contentLower, keyword) && strings.Contains(contentLower, targetLower) {
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

// loadSourceSummaries reads the summary files for the given source paths.
// This provides the LLM with actual source content to ground articles in reality.
func loadSourceSummaries(projectDir, outputDir string, sources []string) string {
	var parts []string
	summariesDir := filepath.Join(projectDir, outputDir, "summaries")

	for _, src := range sources {
		// Source path is like "raw/架构文档/TECH_STACK.md" → summary is "wiki/summaries/TECH_STACK.md"
		base := filepath.Base(src)
		summaryPath := filepath.Join(summariesDir, base)

		data, err := os.ReadFile(summaryPath)
		if err != nil {
			continue
		}

		content := string(data)
		// Strip frontmatter from summary before injecting
		content = stripFrontmatter(content)
		if strings.TrimSpace(content) == "" {
			continue
		}

		parts = append(parts, fmt.Sprintf("### Source: %s\n%s", src, strings.TrimSpace(content)))
	}

	return strings.Join(parts, "\n\n")
}

func sanitizeID(s string) string {
	return strings.NewReplacer("/", "-", "\\", "-", ".", "-", " ", "-").Replace(s)
}

// stripOuterCodeFence removes ```markdown ... ``` or ``` ... ``` wrapping from LLM output.
func stripOuterCodeFence(content string) string {
	trimmed := strings.TrimSpace(content)
	// Match ```markdown, ```md, or plain ```
	if m := reOuterCodeFence.FindStringSubmatch(trimmed); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return content
}

// stripFrontmatter removes YAML frontmatter (--- ... ---) from the beginning of content.
func stripFrontmatter(content string) string {
	trimmed := strings.TrimLeft(content, "\n \t")
	if !strings.HasPrefix(trimmed, "---") {
		return content
	}
	// Find the closing ---
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return content
	}
	return strings.TrimLeft(rest[idx+4:], "\n")
}

// languageDisplayName returns a human-readable language name for prompts.
func languageDisplayName(lang string) string {
	switch strings.ToLower(lang) {
	case "zh", "zh-cn", "chinese":
		return "Chinese (Simplified, 简体中文)"
	case "zh-tw":
		return "Chinese (Traditional, 繁體中文)"
	case "en", "english":
		return "English"
	case "ja", "japanese":
		return "Japanese (日本語)"
	case "ko", "korean":
		return "Korean (한국어)"
	default:
		return lang
	}
}

// normalizeConfidence replaces non-standard confidence values in frontmatter
// with the enum (high/medium/low).
func normalizeConfidence(content string) string {
	// Find confidence line in frontmatter
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "confidence:") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "confidence:"))
			normalized := mapConfidence(value)
			lines[i] = "confidence: " + normalized
			break
		}
	}
	return strings.Join(lines, "\n")
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

// antiPatternPhrases are sentences the LLM generates despite prompt rules forbidding them.
// Post-processing is more reliable than prompt-only prevention for weaker models.
var antiPatternPhrases = []string{
	"源文档未提及",
	"未详细说明",
	"源文档中未",
	"文档未提及",
	"没有明确",
	"no explicit mention",
	"not explicitly discussed",
	"not mentioned in the source",
}

// stripAntiPatternSentences removes sentences containing anti-pattern phrases.
// Operates line-by-line: if a line contains an anti-pattern, the entire line is removed.
// Preserves headings and structural elements.
func stripAntiPatternSentences(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Never remove headings, code blocks, tables, or empty lines
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "```") ||
			strings.HasPrefix(trimmed, "|") ||
			strings.HasPrefix(trimmed, "---") {
			result = append(result, line)
			continue
		}
		// Check for anti-pattern phrases
		lower := strings.ToLower(trimmed)
		hasAnti := false
		for _, ap := range antiPatternPhrases {
			if strings.Contains(lower, strings.ToLower(ap)) {
				hasAnti = true
				break
			}
		}
		if !hasAnti {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// sourceHasCode returns true if the source context contains code blocks or SQL keywords.
func sourceHasCode(sourceContext string) bool {
	return strings.Contains(sourceContext, "```") ||
		strings.Contains(sourceContext, "SELECT ") ||
		strings.Contains(sourceContext, "CREATE TABLE") ||
		strings.Contains(sourceContext, "SUM(") ||
		strings.Contains(sourceContext, "COUNT(")
}

// containsCJK returns true if the string contains any CJK Unified Ideograph character.
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// sanitizeWikilinks converts Chinese wikilinks to English kebab-case using known concept aliases.
// Unknown Chinese links are stripped of brackets (kept as plain text).
func sanitizeWikilinks(content string, knownConcepts map[string]string) string {
	return reWikilink.ReplaceAllStringFunc(content, func(match string) string {
		target := match[2 : len(match)-2]
		if !containsCJK(target) {
			return match
		}
		if kebab, ok := knownConcepts[target]; ok {
			return "[[" + kebab + "]]"
		}
		// No mapping found — remove wikilink markup, keep text
		return target
	})
}

// buildConceptAliasMap constructs a reverse mapping from Chinese aliases to English concept IDs
// for wikilink sanitization.
func buildConceptAliasMap(ontStore *ontology.Store, current ExtractedConcept) map[string]string {
	m := make(map[string]string)
	// Add current concept's own aliases
	for _, alias := range current.Aliases {
		if containsCJK(alias) {
			m[alias] = current.Name
		}
	}
	// Add all known entities from the ontology store
	if ontStore != nil {
		if entities, err := ontStore.ListEntities(""); err == nil {
			for _, e := range entities {
				if containsCJK(e.Name) {
					m[e.Name] = e.ID
				}
			}
		}
	}
	return m
}

// validateWikilinks removes [[links]] that point to non-existent concept articles.
func validateWikilinks(projectDir, outputDir, content string) string {
	conceptsDir := filepath.Join(projectDir, outputDir, "concepts")

	return reWikilink.ReplaceAllStringFunc(content, func(match string) string {
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
