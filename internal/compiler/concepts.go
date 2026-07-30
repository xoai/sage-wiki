package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// ExtractedConcept represents a concept identified by the LLM.
type ExtractedConcept struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Sources []string `json:"sources"`
	Type    string   `json:"type"` // concept, technique, claim
}

// manifestConceptRefs converts the manifest's concept map into a slice of
// ExtractedConcept carrying just the fields needed for co-occurrence discovery
// (Name — the map key — and Sources). The manifest is the authoritative FULL
// concept set at write time (it includes concepts from prior compiles, not
// just the current batch), so the write pass sources related-concept
// candidates from here to cover incremental compiles as well as full ones.
// Aliases ARE stored in the manifest as of #128 (Concept.Aliases); refs
// carry them so alias-overlap dedup can match extracted acronyms against
// existing concepts' aliases.
func manifestConceptRefs(m map[string]manifest.Concept) []ExtractedConcept {
	refs := make([]ExtractedConcept, 0, len(m))
	for name, c := range m {
		refs = append(refs, ExtractedConcept{Name: name, Sources: c.Sources, Aliases: c.Aliases})
	}
	return refs
}

// ExtractConcepts runs Pass 2: concept extraction from summaries.
// It takes new/updated summaries and the existing concept list,
// asks the LLM to identify and deduplicate concepts.
// concurrency > 1 runs batches in parallel; each batch receives the same
// existingConcepts snapshot as dedup context (not the growing allConcepts),
// so deduplicateConcepts at the end handles cross-batch merging.
func ExtractConcepts(
	ctx context.Context,
	summaries []SummaryResult,
	existingConcepts map[string]manifest.Concept,
	client *llm.Client,
	model string,
	batchSize int,
	maxTokens int,
	concurrency int,
) ([]ExtractedConcept, error) {
	defer metrics.ObserveDuration(metrics.HistogramNamed("compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "extract"), time.Now())
	if ctx == nil {
		ctx = context.Background()
	}
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

	// Track batch failures so a total failure surfaces as an error instead of a
	// silent empty result (the caller only increments result.Errors when this
	// returns non-nil). firstErr carries the actionable diagnostic.
	var failMu sync.Mutex
	failures := 0
	var firstErr error
	recordFailure := func(e error) {
		failMu.Lock()
		failures++
		if firstErr == nil {
			firstErr = e
		}
		failMu.Unlock()
	}

	for _, b := range batches {
		wg.Add(1)
		go func(b batchWork) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Don't start a batch's LLM call if the compile was cancelled while
			// this goroutine waited for a concurrency slot.
			if ctx.Err() != nil {
				return
			}

			log.Info("extracting concepts batch", "batch", b.index+1, "of", totalBatches, "summaries", len(b.items))

			var summaryTexts []string
			for _, s := range b.items {
				summary := s.Summary
				if len(summary) > 1000 {
					summary = summary[:1000] + "\n..."
				}
				// Summaries are LLM output over untrusted text — neutralize
				// spoof delimiter tags (second-order injection, SEC-04 site 4)
				// before they join the extract_concepts template's frame.
				// Applied to the whole line (path included) for consistency
				// with buildSourceContext — a filename may legally contain
				// the opening tag on Linux.
				summaryTexts = append(summaryTexts, prompts.NeutralizeTags(fmt.Sprintf("### Source: %s\n%s", s.SourcePath, summary)))
			}

			prompt, err := prompts.Render("extract_concepts", prompts.ExtractData{
				ExistingConcepts: dedupSnapshot,
				Summaries:        strings.Join(summaryTexts, "\n\n---\n\n"),
			}, "")
			if err != nil {
				recordFailure(fmt.Errorf("batch %d render: %w", b.index+1, err))
				log.Error("render extract_concepts prompt failed", "batch", b.index+1, "error", err)
				return
			}

			// P2-4: schema-guaranteed JSON where the provider supports it;
			// fallback = plain completion + shared fence-strip (same
			// tolerance as the old parser; the empty-content hint
			// (finish_reason) is threaded through StructuredCompletion on
			// both paths (decisions.md 2026-07-23).
			payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
				{Role: "system", Content: "You are a concept extraction system for a knowledge wiki. Output valid JSON only."},
				{Role: "user", Content: prompt},
			}, ConceptsSchema, llm.CallOpts{Model: model, MaxTokens: maxTokens})
			if err != nil {
				recordFailure(fmt.Errorf("batch %d: %w", b.index+1, err))
				log.Error("concept extraction batch failed", "batch", b.index+1, "error", err)
				return
			}

			var concepts []ExtractedConcept
			if err := json.Unmarshal(payload, &concepts); err != nil {
				recordFailure(fmt.Errorf("batch %d parse: %w", b.index+1, err))
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
	allConcepts = deduplicateConcepts(allConcepts, existingConcepts)

	// A total failure (every batch errored) must not look like a clean empty
	// extraction — return an error so the caller increments result.Errors instead
	// of silently skipping article writing. Partial failures proceed with what
	// did extract.
	if failures > 0 {
		if failures == totalBatches {
			return nil, fmt.Errorf("concept extraction failed: all %d batch(es) errored: %w", totalBatches, firstErr)
		}
		log.Warn("some concept-extraction batches failed", "failed", failures, "of", totalBatches)
	}

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

// normalizeName canonicalizes a concept/alias name for matching: lowercase,
// trimmed. Applied to ALL comparisons including within-set keys (issue #128 —
// deliberate change from raw c.Name keys, so "RAP " and "rap" unify).
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// deduplicateConcepts merges concepts across batches: exact-normalized
// name==name (as before), PLUS alias overlap in both directions (issue
// #128). An extracted concept whose name matches an existing concept's
// alias — within the batch or in the manifest — folds into the canonical
// concept instead of standing alone (the bare-acronym case).
func deduplicateConcepts(concepts []ExtractedConcept, existing map[string]manifest.Concept) []ExtractedConcept {
	// Manifest alias index: alias → canonical name (deterministic: sorted
	// manifest keys, first alias wins).
	manifestAlias := map[string]string{}
	var mKeys []string
	for k := range existing {
		mKeys = append(mKeys, k)
	}
	sort.Strings(mKeys)
	for _, name := range mKeys {
		for _, a := range existing[name].Aliases {
			if _, ok := manifestAlias[normalizeName(a)]; !ok {
				manifestAlias[normalizeName(a)] = name
			}
		}
	}

	seen := map[string]*ExtractedConcept{} // normalized name → canonical entry
	var result []ExtractedConcept

	merge := func(dst, src *ExtractedConcept) {
		srcSet := map[string]bool{}
		for _, s := range dst.Sources {
			srcSet[s] = true
		}
		for _, s := range src.Sources {
			if !srcSet[s] {
				dst.Sources = append(dst.Sources, s)
			}
		}
		aliasSet := map[string]bool{}
		for _, a := range dst.Aliases {
			aliasSet[a] = true
		}
		for _, a := range src.Aliases {
			if !aliasSet[a] {
				dst.Aliases = append(dst.Aliases, a)
			}
		}
		if !aliasSet[src.Name] && normalizeName(src.Name) != normalizeName(dst.Name) {
			dst.Aliases = append(dst.Aliases, src.Name) // loser's name becomes an alias
		}
	}

	for _, c := range concepts {
		key := normalizeName(c.Name)
		// Rule 2: acronym matches a manifest concept's alias → rename to
		// canonical and carry union(manifest sources+aliases, A's).
		if canonical, ok := manifestAlias[key]; ok {
			if existing, ok := seen[key]; ok {
				merge(existing, &c)
				continue
			}
			mc := existing[canonical]
			merged := ExtractedConcept{
				Name:    canonical,
				Sources: append(append([]string(nil), mc.Sources...), c.Sources...),
				Aliases: append(append([]string(nil), mc.Aliases...), c.Aliases...),
				Type:    c.Type,
			}
			// Union aliases + the extracted name (dedup).
			merged.Aliases = append(merged.Aliases, c.Name)
			seen[key] = &merged
			seen[normalizeName(canonical)] = &merged
			result = append(result, merged)
			log.Info("dedup: folded into existing concept", "from", c.Name, "into", canonical)
			continue
		}
		if existing, ok := seen[key]; ok {
			merge(existing, &c)
			continue
		}
		// Rule 1: new concept's name is another entry's alias (either direction).
		folded := false
		for ri, r := range result {
			aliasRelated := false
			for _, a := range r.Aliases {
				if normalizeName(a) == key {
					aliasRelated = true
					break
				}
			}
			if !aliasRelated {
				for _, a := range c.Aliases {
					if normalizeName(a) == normalizeName(r.Name) {
						aliasRelated = true
						break
					}
				}
			}
			if !aliasRelated {
				continue
			}
			// Canonical = the longer normalized name (the expansion, not the
			// acronym) — "remedial-action-plan" beats "rap" either way.
			if len(normalizeName(c.Name)) > len(normalizeName(r.Name)) {
				merge(&c, &r)
				result[ri] = c
				delete(seen, normalizeName(r.Name))
				seen[key] = &result[ri]
				log.Info("dedup: folded into existing concept", "from", r.Name, "into", c.Name)
			} else {
				merge(seen[normalizeName(r.Name)], &c)
				log.Info("dedup: folded into existing concept", "from", c.Name, "into", r.Name)
			}
			folded = true
			break
		}
		if folded {
			continue
		}
		copy := c
		seen[key] = &copy
		result = append(result, copy)
	}

	// Apply merged data back (preserves prior behavior for exact-name merges).
	for i := range result {
		if merged, ok := seen[normalizeName(result[i].Name)]; ok {
			result[i] = *merged
		}
	}
	return result
}

// ConceptsSchema is the canonical schema for concept extraction (P2-4).
var ConceptsSchema = llm.JSONSchema{
	Name:        "concepts",
	Description: "concepts extracted from the source text",
	IsArray:     true,
	Schema: map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"aliases": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"sources": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"type": map[string]any{"type": "string"},
			},
			"required": []string{"name", "sources", "type"},
		},
		"minItems": 0,
	},
}
