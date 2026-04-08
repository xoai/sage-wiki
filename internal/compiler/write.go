package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
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

			result := writeOneArticle(projectDir, outputDir, c, client, model, maxTokens, memStore, vecStore, ontStore, embedder)
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
	prompt := buildArticlePrompt(concept, existingContent, relatedNames)

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are a wiki author writing comprehensive, precise articles for a personal knowledge base. Use YAML frontmatter and [[wikilinks]]."},
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		result.Error = fmt.Errorf("llm call: %w", err)
		return result
	}

	articleContent := resp.Content

	// Ensure frontmatter exists
	if !strings.HasPrefix(articleContent, "---") {
		articleContent = buildFrontmatter(concept) + "\n\n" + articleContent
	}

	// Normalize confidence values to enum (high/medium/low)
	articleContent = normalizeConfidence(articleContent)

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
	extractRelations(concept.Name, articleContent, ontStore)

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

func buildArticlePrompt(concept ExtractedConcept, existing string, related []string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Write a comprehensive wiki article about: %s\n\n", formatConceptName(concept.Name)))
	b.WriteString(fmt.Sprintf("Concept ID: %s\n", concept.Name))

	if len(concept.Aliases) > 0 {
		b.WriteString(fmt.Sprintf("Also known as: %s\n", strings.Join(concept.Aliases, ", ")))
	}

	b.WriteString(fmt.Sprintf("Sources: %s\n", strings.Join(concept.Sources, ", ")))

	if len(related) > 0 {
		b.WriteString(fmt.Sprintf("Related concepts: %s\n", strings.Join(related, ", ")))
	}

	if existing != "" {
		b.WriteString("\n## Existing article (update/expand):\n")
		b.WriteString(existing)
		b.WriteString("\n")
	}

	b.WriteString(`
Write the article with:
1. YAML frontmatter with these exact fields:
   - concept: (the concept ID)
   - aliases: (alternative names)
   - sources: (source file paths)
   - confidence: MUST be exactly one of: high, medium, low (no numbers, no percentages)
2. ## Definition — clear, precise definition
3. ## How it works — technical explanation
4. ## Variants — known variants or implementations if any
5. ## Trade-offs — key trade-offs or limitations
6. ## See also — [[wikilinks]] to related concepts

IMPORTANT rules for wikilinks:
- Use [[concept-name]] format (lowercase-hyphenated)
- Link to any concept that deserves a standalone article — even if the article doesn't exist yet (it will be created in future compiles)
- Do NOT link to generic terms, math notation ($O(n)$), or register names ($a0)
- Each link should be a meaningful technical concept, not filler

For the concept's relationship to other concepts, indicate the relationship type in your text:
- "X implements Y" / "X extends Y" / "X optimizes Y"
- "X contradicts Y" / "X is a prerequisite for Y"
This helps build the knowledge graph.`)

	return b.String()
}

func buildFrontmatter(concept ExtractedConcept) string {
	aliases := quoteYAMLList(concept.Aliases)
	sources := quoteYAMLList(concept.Sources)

	return fmt.Sprintf(`---
concept: %s
aliases: %s
sources: %s
confidence: medium
created_at: %s
---`, concept.Name, aliases, sources, timeNow())
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
func extractRelations(conceptID string, content string, ontStore *ontology.Store) {
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

	// Pattern matching for relation types
	relationPatterns := []struct {
		keywords []string
		relation string
	}{
		{[]string{"implements", "implementation of", "is an implementation"}, ontology.RelImplements},
		{[]string{"extends", "extension of", "builds on", "builds upon"}, ontology.RelExtends},
		{[]string{"optimizes", "optimization of", "improves upon", "faster than"}, ontology.RelOptimizes},
		{[]string{"contradicts", "conflicts with", "disagrees with", "challenges"}, ontology.RelContradicts},
		{[]string{"prerequisite", "requires knowledge of", "depends on", "built on top of"}, ontology.RelPrerequisiteOf},
		{[]string{"trade-off", "tradeoff", "trades off", "at the cost of"}, ontology.RelTradesOff},
	}

	for target := range linkedConcepts {
		targetLower := strings.ToLower(target)
		for _, rp := range relationPatterns {
			for _, keyword := range rp.keywords {
				// Look for the keyword near the concept mention
				if strings.Contains(contentLower, keyword) && strings.Contains(contentLower, targetLower) {
					ontStore.AddRelation(ontology.Relation{
						ID:       conceptID + "-" + rp.relation + "-" + target,
						SourceID: conceptID,
						TargetID: target,
						Relation: rp.relation,
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
