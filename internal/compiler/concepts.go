package compiler

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// ExtractedConcept represents a concept identified by the LLM.
type ExtractedConcept struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Sources []string `json:"sources"`
	Type    string   `json:"type"` // concept, technique, claim
}

// ExtractConcepts runs Pass 2: concept extraction from summaries.
// It takes new/updated summaries and the existing concept list,
// asks the LLM to identify and deduplicate concepts.
// concurrency > 1 runs batches in parallel; each batch receives the same
// existingConcepts snapshot as dedup context (not the growing allConcepts),
// so deduplicateConcepts at the end handles cross-batch merging.
func ExtractConcepts(
	summaries []SummaryResult,
	existingConcepts map[string]manifest.Concept,
	client *llm.Client,
	model string,
	batchSize int,
	maxTokens int,
	concurrency int,
) ([]ExtractedConcept, error) {
	if batchSize <= 0 {
		batchSize = 20
	}
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	if concurrency <= 1 {
		concurrency = 1
	}
	if len(summaries) == 0 {
		return nil, nil
	}

	// Filter valid summaries
	var validSummaries []SummaryResult
	for _, s := range summaries {
		if s.Error == nil && s.Summary != "" {
			validSummaries = append(validSummaries, s)
		}
	}
	if len(validSummaries) == 0 {
		return nil, nil
	}

	// Build existing concept list for dedup context (shared snapshot for all batches)
	var existingList []string
	for name := range existingConcepts {
		existingList = append(existingList, name)
	}
	dedupSnapshot := strings.Join(existingList, ", ")

	// Split into batches
	type batchWork struct {
		index int
		items []SummaryResult
	}
	var batches []batchWork
	for i := 0; i < len(validSummaries); i += batchSize {
		end := i + batchSize
		if end > len(validSummaries) {
			end = len(validSummaries)
		}
		batches = append(batches, batchWork{index: i / batchSize, items: validSummaries[i:end]})
	}

	totalBatches := len(batches)
	results := make([][]ExtractedConcept, totalBatches)
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for _, b := range batches {
		wg.Add(1)
		go func(b batchWork) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			log.Info("extracting concepts batch", "batch", b.index+1, "of", totalBatches, "summaries", len(b.items))

			var summaryTexts []string
			for _, s := range b.items {
				summary := s.Summary
				if len(summary) > 1000 {
					summary = summary[:1000] + "\n..."
				}
				summaryTexts = append(summaryTexts, fmt.Sprintf("### Source: %s\n%s", s.SourcePath, summary))
			}

			prompt, err := prompts.Render("extract_concepts", prompts.ExtractData{
				ExistingConcepts: dedupSnapshot,
				Summaries:        strings.Join(summaryTexts, "\n\n---\n\n"),
			}, "")
			if err != nil {
				log.Error("render extract_concepts prompt failed", "batch", b.index+1, "error", err)
				return
			}

			resp, err := client.ChatCompletion([]llm.Message{
				{Role: "system", Content: "You are a concept extraction system for a knowledge wiki. Output valid JSON only."},
				{Role: "user", Content: prompt},
			}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
			if err != nil {
				log.Error("concept extraction batch failed", "batch", b.index+1, "error", err)
				return
			}

			concepts, err := parseConceptsJSON(resp.Content)
			if err != nil {
				log.Error("concept extraction parse failed", "batch", b.index+1, "error", err)
				return
			}

			results[b.index] = concepts
			log.Info("batch concepts extracted", "batch", b.index+1, "count", len(concepts))
		}(b)
	}

	wg.Wait()

	// Flatten results in original batch order
	var allConcepts []ExtractedConcept
	for _, r := range results {
		allConcepts = append(allConcepts, r...)
	}

	// Filter noise
	allConcepts = filterNoisyConcepts(allConcepts)

	// Deduplicate across batches
	allConcepts = deduplicateConcepts(allConcepts)

	log.Info("concepts extracted", "total", len(allConcepts))
	return allConcepts, nil
}

// filterNoisyConcepts removes concepts that are likely noise (LaTeX, registers, etc.).
func filterNoisyConcepts(concepts []ExtractedConcept) []ExtractedConcept {
	var filtered []ExtractedConcept
	for _, c := range concepts {
		name := c.Name
		// Skip very short names (likely abbreviations or noise)
		if len(name) < 2 {
			continue
		}
		// Skip names that look like math notation
		if strings.Contains(name, "$") || strings.Contains(name, "\\") {
			continue
		}
		// Skip names that look like register names ($a0, $t1)
		if strings.HasPrefix(name, "$") {
			continue
		}
		// Skip names that are just numbers
		isAllDigits := true
		for _, r := range name {
			if r < '0' || r > '9' {
				isAllDigits = false
				break
			}
		}
		if isAllDigits {
			continue
		}
		// Skip names that look like file paths
		if strings.Contains(name, "/") || strings.Contains(name, ".md") {
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered
}

// deduplicateConcepts merges concepts with the same name across batches.
func deduplicateConcepts(concepts []ExtractedConcept) []ExtractedConcept {
	seen := map[string]*ExtractedConcept{}
	var result []ExtractedConcept

	for _, c := range concepts {
		if existing, ok := seen[c.Name]; ok {
			// Merge sources
			srcSet := map[string]bool{}
			for _, s := range existing.Sources {
				srcSet[s] = true
			}
			for _, s := range c.Sources {
				if !srcSet[s] {
					existing.Sources = append(existing.Sources, s)
				}
			}
			// Merge aliases
			aliasSet := map[string]bool{}
			for _, a := range existing.Aliases {
				aliasSet[a] = true
			}
			for _, a := range c.Aliases {
				if !aliasSet[a] {
					existing.Aliases = append(existing.Aliases, a)
				}
			}
		} else {
			copy := c
			seen[c.Name] = &copy
			result = append(result, copy)
		}
	}

	// Apply merged data back
	for i := range result {
		if merged, ok := seen[result[i].Name]; ok {
			result[i] = *merged
		}
	}

	return result
}

// parseConceptsJSON extracts a JSON array from the LLM response.
// Handles cases where the LLM wraps JSON in markdown code fences.
func parseConceptsJSON(text string) ([]ExtractedConcept, error) {
	text = strings.TrimSpace(text)

	// Strip markdown code fences if present
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		text = strings.Join(jsonLines, "\n")
	}

	// Find the JSON array
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var concepts []ExtractedConcept
	if err := json.Unmarshal([]byte(text), &concepts); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w\nraw: %s", err, text[:min(200, len(text))])
	}

	return concepts, nil
}

