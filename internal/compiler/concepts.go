package compiler

import (
	"encoding/json"
	"fmt"
	"strings"

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
const conceptBatchSize = 20 // summaries per LLM call

func ExtractConcepts(
	summaries []SummaryResult,
	existingConcepts map[string]manifest.Concept,
	client *llm.Client,
	model string,
) ([]ExtractedConcept, error) {
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

	// Build existing concept list for dedup context
	var existingList []string
	for name := range existingConcepts {
		existingList = append(existingList, name)
	}

	// Process in batches
	var allConcepts []ExtractedConcept

	for i := 0; i < len(validSummaries); i += conceptBatchSize {
		end := i + conceptBatchSize
		if end > len(validSummaries) {
			end = len(validSummaries)
		}
		batch := validSummaries[i:end]

		log.Info("extracting concepts batch", "batch", i/conceptBatchSize+1, "summaries", len(batch), "total", len(validSummaries))

		var summaryTexts []string
		for _, s := range batch {
			// Use truncated summary to stay within context limits
			summary := s.Summary
			if len(summary) > 1000 {
				summary = summary[:1000] + "\n..."
			}
			summaryTexts = append(summaryTexts, fmt.Sprintf("### Source: %s\n%s", s.SourcePath, summary))
		}

		// Include previously extracted concepts in the dedup list
		dedup := make([]string, len(existingList))
		copy(dedup, existingList)
		for _, c := range allConcepts {
			dedup = append(dedup, c.Name)
		}

		prompt, err := prompts.Render("extract_concepts", prompts.ExtractData{
			ExistingConcepts: strings.Join(dedup, ", "),
			Summaries:        strings.Join(summaryTexts, "\n\n---\n\n"),
		}, "")
		if err != nil {
			log.Error("render extract_concepts prompt failed", "batch", i/conceptBatchSize+1, "error", err)
			continue
		}

		resp, err := client.ChatCompletion([]llm.Message{
			{Role: "system", Content: "You are a concept extraction system for a knowledge wiki. Output valid JSON only."},
			{Role: "user", Content: prompt},
		}, llm.CallOpts{Model: model, MaxTokens: 8192})
		if err != nil {
			log.Error("concept extraction batch failed", "batch", i/conceptBatchSize+1, "error", err)
			continue // skip failed batch, continue with others
		}

		concepts, err := parseConceptsJSON(resp.Content)
		if err != nil {
			log.Error("concept extraction parse failed", "batch", i/conceptBatchSize+1, "error", err)
			continue
		}

		allConcepts = append(allConcepts, concepts...)
		log.Info("batch concepts extracted", "batch", i/conceptBatchSize+1, "count", len(concepts))
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

