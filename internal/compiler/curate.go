package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// Concept curation pass (issue #167): the one stage that sees the FULL
// newly-extracted concept set plus the existing manifest, and decides —
// keep / fold / drop — per concept. Replaces embedding dedup when
// dedup_strategy: "llm" (either/or; running both is incoherent — embedding
// merges would pre-empt judgment).
//
// Safety posture (spec D2):
//   - folds auto-apply (additive: sources union + alias recorded)
//   - drops need llm_dedup.allow_drop (destructive otherwise; logged as
//     unapplied proposals)
//   - neverMergeNames (#164) VETOES model-proposed folds between enumerated
//     identities — the deterministic floor under judgment
//   - unknown actions / dangling fold targets keep the concept

// CurateSchema is the canonical schema for the curation decision (P2-4).
var CurateSchema = llm.JSONSchema{
	Name:        "curation_actions",
	Description: "one action per proposed concept",
	IsArray:     true,
	Schema: map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   map[string]any{"type": "string"},
				"action": map[string]any{"type": "string", "enum": []string{"keep", "fold", "drop"}},
				"into":   map[string]any{"type": "string"},
				"alias":  map[string]any{"type": "string"},
				"reason": map[string]any{"type": "string"},
			},
			"required": []string{"name", "action"},
		},
		"minItems": 0,
	},
}

type curateAction struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Into   string `json:"into"`
	Alias  string `json:"alias"`
	Reason string `json:"reason"`
}

// curateConcepts runs the curation pass over newly extracted concepts.
// existingNames are fold targets from the manifest; mf receives folds into
// existing concepts (mergeConceptIntoManifest — union semantics). Returns
// the surviving concept list. A TOTAL failure of every curation call returns
// an error; callers count it (result.Errors++) and proceed uncurated — an
// opt-in advisory pass must not block a compile whose extraction succeeded.
func curateConcepts(
	ctx context.Context,
	client *llm.Client,
	model string,
	pr *prompts.Registry,
	newConcepts []ExtractedConcept,
	existingNames []string,
	allowDrop bool,
	batchSize int,
	mf *manifest.Manifest,
) ([]ExtractedConcept, error) {
	if len(newConcepts) == 0 || batchSize <= 0 {
		return newConcepts, nil
	}

	// SPEC-04: every ordering sorted — chunk contents, existing names, and
	// the accumulated keeps fed to later chunks.
	sortedNew := make([]ExtractedConcept, len(newConcepts))
	copy(sortedNew, newConcepts)
	sort.Slice(sortedNew, func(i, j int) bool { return sortedNew[i].Name < sortedNew[j].Name })
	sortedExisting := append([]string(nil), existingNames...)
	sort.Strings(sortedExisting)

	failures, total := 0, 0
	firstErr := error(nil)
	droppedNames := map[string]bool{}

	for start := 0; start < len(sortedNew); start += batchSize {
		end := start + batchSize
		if end > len(sortedNew) {
			end = len(sortedNew)
		}
		chunk := sortedNew[start:end]
		total++

		actions, err := curateChunk(ctx, client, model, pr, chunk, sortedExisting)
		if err != nil {
			failures++
			if firstErr == nil {
				firstErr = err
			}
			continue // this chunk proceeds uncurated
		}

		byName := map[string]curateAction{}
		for _, a := range actions {
			byName[a.Name] = a
		}

		for i := range chunk {
			c := &chunk[i]
			a, ok := byName[c.Name]
			if !ok {
				continue // no action returned: keep (never destroy on silence)
			}
			switch a.Action {
			case "fold":
				applyCurateFold(c, a, sortedExisting, &chunk, mf, droppedNames)
			case "drop":
				if allowDrop {
					droppedNames[c.Name] = true
					log.Warn("concept curation: drop", "name", c.Name, "reason", a.Reason)
				} else {
					log.Warn("concept curation: drop proposed but allow_drop=false — kept", "name", c.Name, "reason", a.Reason)
				}
			default: // keep, unknown action: no-op
			}
		}
	}

	if failures > 0 && failures == total {
		return nil, fmt.Errorf("concept curation failed: all %d call(s) errored: %w", total, firstErr)
	}
	if failures > 0 {
		log.Warn("concept curation: some calls failed — those concepts proceed uncurated", "failed", failures, "of", total)
	}

	var kept []ExtractedConcept
	for _, c := range sortedNew {
		if droppedNames[c.Name] {
			continue
		}
		kept = append(kept, c)
	}
	return kept, nil
}

// applyCurateFold applies one fold proposal with the #164 veto. Targets may
// be an existing manifest concept or another proposed concept in the same
// chunk; dangling or vetoed targets keep the concept.
func applyCurateFold(c *ExtractedConcept, a curateAction, existing []string, chunk *[]ExtractedConcept, mf *manifest.Manifest, droppedNames map[string]bool) {
	target := strings.TrimSpace(a.Into)
	if target == "" || target == c.Name {
		return
	}
	if neverMergeNames(c.Name, target) {
		// The deterministic floor: enumerated identities never fold, no
		// matter what the model says.
		log.Warn("concept curation: fold VETOED by never-merge guard (#164)", "name", c.Name, "into", target)
		return
	}
	alias := strings.TrimSpace(a.Alias)
	if alias == "" {
		alias = c.Name
	}

	// Existing manifest concept wins as the target (stable across compiles).
	if mf != nil {
		if _, ok := mf.Concepts[target]; ok {
			droppedNames[c.Name] = true // folded away; not emitted as its own concept
			mergeConceptIntoManifest(mf, target, *c)
			// ensure the alias records the folded name (merge unions c.Aliases;
			// the fold alias rides along if absent)
			if existing, ok2 := mf.Concepts[target]; ok2 && !containsStr(existing.Aliases, alias) {
				existing.Aliases = append(existing.Aliases, alias)
				mf.Concepts[target] = existing
			}
			log.Warn("concept curation: fold into existing", "name", c.Name, "into", target, "alias", alias)
			return
		}
	}
	_ = existing

	// In-set target: fold into the other proposed concept. Canonical = the
	// LONGER name (alias-overlap convention: expansions beat short forms).
	for i := range *chunk {
		other := &(*chunk)[i]
		if other.Name != target || droppedNames[other.Name] || other == c {
			continue
		}
		from, to := c, other
		if len(from.Name) > len(to.Name) {
			// c has the longer name: canonical flips — fold `other` into c.
			from, to = other, c
			droppedNames[from.Name] = true
			absorbConcept(to, from, aliasFor(from.Name, a))
			log.Warn("concept curation: fold (canonical=longer)", "name", from.Name, "into", to.Name)
			return
		}
		droppedNames[c.Name] = true
		absorbConcept(other, c, alias)
		log.Warn("concept curation: fold (canonical=longer)", "name", c.Name, "into", other.Name)
		return
	}
	// Dangling target (not in manifest, not in chunk): keep — never destroy
	// on a reference we cannot resolve.
	log.Warn("concept curation: fold target not found — kept", "name", c.Name, "into", target)
}

func aliasFor(defaultAlias string, a curateAction) string {
	if s := strings.TrimSpace(a.Alias); s != "" {
		return s
	}
	return defaultAlias
}

// absorbConcept unions src's sources/aliases into dst and records alias.
func absorbConcept(dst, src *ExtractedConcept, alias string) {
	dst.Sources = unionStr(dst.Sources, src.Sources)
	dst.Aliases = unionStr(dst.Aliases, src.Aliases)
	if alias != "" && !containsStr(dst.Aliases, alias) {
		dst.Aliases = append(dst.Aliases, alias)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func unionStr(dst, src []string) []string {
	seen := map[string]bool{}
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if !seen[s] {
			dst = append(dst, s)
			seen[s] = true
		}
	}
	return dst
}

// maybeCurateConcepts is the single wiring point for the curation pass
// (#167) — called at the post-evidence-gate seam on ALL write paths
// (fullpipeline, reextract, resumeBatch). One helper so the three sites
// cannot diverge (the #106/#128 three-sites lesson, honored by
// construction). No-op unless dedup_strategy is "llm"; failure is advisory:
// result.Errors++ and the concepts proceed uncurated.
func maybeCurateConcepts(
	ctx context.Context,
	client *llm.Client,
	model string,
	pr *prompts.Registry,
	concepts []ExtractedConcept,
	mf *manifest.Manifest,
	cfg *config.Config,
) ([]ExtractedConcept, error) {
	if cfg == nil || cfg.Compiler.DedupStrategy != "llm" || len(concepts) == 0 {
		return concepts, nil
	}
	mfNames := make([]string, 0, len(mf.Concepts))
	for name := range mf.Concepts {
		mfNames = append(mfNames, name)
	}
	sort.Strings(mfNames) // SPEC-04: manifest map order must not reach the prompt
	return curateConcepts(ctx, client, model, pr, concepts, mfNames,
		cfg.Compiler.LLMDedup.AllowDropOrDefault(), cfg.Compiler.LLMDedup.BatchSizeOrDefault(), mf)
}

// curateChunk renders the prompt for one chunk and parses the action array.
func curateChunk(ctx context.Context, client *llm.Client, model string, pr *prompts.Registry, chunk []ExtractedConcept, existing []string) ([]curateAction, error) {
	var lines []string
	for _, c := range chunk {
		lines = append(lines, fmt.Sprintf("- %s | %s | %s | %d source(s)", c.Name, c.Type, strings.Join(c.Aliases, ", "), len(c.Sources)))
	}
	prompt, err := renderPrompt(pr, "curate_concepts", prompts.CurateData{
		ExistingConcepts: strings.Join(existing, ", "),
		Proposed:         strings.Join(lines, "\n"),
	}, "")
	if err != nil {
		return nil, err
	}
	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: "You are a concept curation system. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, CurateSchema, llm.CallOpts{Model: model, MaxTokens: 8192})
	if err != nil {
		return nil, err
	}
	var actions []curateAction
	if err := json.Unmarshal(payload, &actions); err != nil {
		return nil, fmt.Errorf("curate parse: %w", err)
	}
	return actions, nil
}
