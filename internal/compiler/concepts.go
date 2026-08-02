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
	for _, name := range sortedConceptNames(m) {
		c := m[name]
		refs = append(refs, ExtractedConcept{Name: name, Sources: c.Sources, Aliases: c.Aliases})
	}
	return refs
}

// sortedConceptNames returns the map keys in canonical (ascending) order.
// SPEC-04 D1: any slice derived from map iteration that feeds bytes,
// prompts, merge decisions, or output order is sorted before use.
func sortedConceptNames(m map[string]manifest.Concept) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	pr *prompts.Registry,
	temp *float64,
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
	// SPEC-04 D1: sorted — map iteration order must not leak into prompt bytes.
	dedupSnapshot := strings.Join(sortedConceptNames(existingConcepts), ", ")

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

			prompt, err := renderPrompt(pr, "extract_concepts", prompts.ExtractData{
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
			}, ConceptsSchema, llm.CallOpts{Model: model, MaxTokens: maxTokens, Temperature: temp})
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

	// seen maps normalized name → the ONE heap entry per canonical concept;
	// result holds pointers into the same entries, so every merge is visible
	// to every later alias check (transitive) and nothing reads stale copies
	// (gates i1: the loop-copy/apply-back pattern lost accumulated merges and
	// aliased a growable slice element).
	seen := map[string]*ExtractedConcept{}
	var result []*ExtractedConcept

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
		// Alias dedup keys are NORMALIZED (review: "RAP" and "rap" must not
		// accumulate as distinct aliases), and the canonical's own name must
		// never land in its alias list (review: self-alias polluted the
		// manifest on A→B→C→A chains).
		aliasSet := map[string]bool{normalizeName(dst.Name): true}
		for _, a := range dst.Aliases {
			aliasSet[normalizeName(a)] = true
		}
		for _, a := range src.Aliases {
			if !aliasSet[normalizeName(a)] {
				aliasSet[normalizeName(a)] = true
				dst.Aliases = append(dst.Aliases, a)
			}
		}
		if !aliasSet[normalizeName(src.Name)] && normalizeName(src.Name) != normalizeName(dst.Name) {
			dst.Aliases = append(dst.Aliases, src.Name) // loser's name becomes an alias
		}
	}

	for i := range concepts {
		c := &concepts[i]
		key := normalizeName(c.Name)
		// Rule 2: acronym matches a manifest concept's alias → fold into the
		// canonical concept, carrying union(manifest sources+aliases, A's)
		// deduped via merge(). A SECOND acronym hitting the same canonical
		// merges into the same entry (gates i1 Major 2).
		if canonical, ok := manifestAlias[key]; ok {
			entry := seen[key]
			if entry == nil {
				entry = seen[normalizeName(canonical)]
			}
			if entry != nil {
				merge(entry, c)
				continue
			}
			mc := existing[canonical]
			entry = &ExtractedConcept{Name: canonical, Type: c.Type}
			merge(entry, &ExtractedConcept{Sources: mc.Sources, Aliases: mc.Aliases})
			merge(entry, c)
			seen[key] = entry
			seen[normalizeName(canonical)] = entry
			result = append(result, entry)
			log.Info("dedup: folded into existing concept", "from", c.Name, "into", canonical)
			continue
		}
		if existingEntry, ok := seen[key]; ok {
			merge(existingEntry, c)
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
				winner := &ExtractedConcept{Name: c.Name, Type: c.Type}
				if winner.Type == "" {
					winner.Type = r.Type // don't drop the loser's type (review)
				}
				merge(winner, r) // r is the heap accumulator — no stale copy
				merge(winner, c)
				// Purge EVERY seen key pointing at the loser, not just its
				// canonical name: rule-2 entries are double-registered
				// (acronym key + canonical key), and a stale key would merge
				// a later acronym into the detached loser (review M1). The
				// purged keys are re-registered at the winner, so a later
				// rule-2 hit on the same canonical still finds it.
				var loserKeys []string
				for k, v := range seen {
					if v == r {
						loserKeys = append(loserKeys, k)
						delete(seen, k)
					}
				}
				for _, k := range loserKeys {
					seen[k] = winner
				}
				seen[key] = winner
				result[ri] = winner
				log.Info("dedup: folded into existing concept", "from", r.Name, "into", c.Name)
			} else {
				merge(r, c)
				log.Info("dedup: folded into existing concept", "from", c.Name, "into", r.Name)
			}
			folded = true
			break
		}
		if folded {
			continue
		}
		entry := &ExtractedConcept{Name: c.Name, Aliases: c.Aliases, Sources: c.Sources, Type: c.Type}
		seen[key] = entry
		result = append(result, entry)
	}

	out := make([]ExtractedConcept, len(result))
	for i, r := range result {
		out[i] = *r
	}
	return out
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
