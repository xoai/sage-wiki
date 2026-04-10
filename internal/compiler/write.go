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
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// ArticleResult holds the output of writing a concept article.
type ArticleResult struct {
	ConceptName string
	ArticlePath string
	Error       error
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

			result := writeOneArticle(projectDir, outputDir, c, client, model, maxTokens, memStore, vecStore, ontStore, embedder, userTZ, articleFields, relationPatterns)
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
) ArticleResult {
	result := ArticleResult{ConceptName: concept.Name}

	// Check for existing article
	articlePath := filepath.Join(outputDir, "concepts", concept.Name+".md")
	absPath := filepath.Join(projectDir, articlePath)
	var existingContent string
	if data, err := os.ReadFile(absPath); err == nil {
		existingContent = string(data)
	}

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
		MaxTokens:       maxTokens,
	})
	if err != nil {
		result.Error = fmt.Errorf("render write_article prompt: %w", err)
		return result
	}

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are a wiki author writing comprehensive, precise articles for a personal knowledge base. Use [[wikilinks]] for cross-references. Do not include YAML frontmatter."},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		result.Error = fmt.Errorf("llm call: %w", err)
		return result
	}

	articleContent := resp.Content

	// Strip any LLM-generated frontmatter — code builds frontmatter from ground-truth data.
	articleContent = stripLLMFrontmatter(articleContent)

	// Extract LLM-judged fields (confidence + any custom fields from config)
	fields, articleContent := extractFields(articleContent, articleFields)

	// Build frontmatter: ground-truth fields + LLM-judged fields
	articleContent = buildFrontmatter(concept, fields, articleFields, userTZ) + "\n\n" + articleContent

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
