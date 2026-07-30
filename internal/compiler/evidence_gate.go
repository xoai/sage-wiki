package compiler

import (
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
)

// filterLowEvidence splits extracted concepts into those with enough
// declared sources to earn an article and those without (issue #128). A
// source-less concept is noise everywhere — no manifest entry, no article,
// no entity, no cites — so this runs before AddConcept on every article
// path. minSources <= 0 disables the gate entirely (everything kept).
func filterLowEvidence(concepts []ExtractedConcept, minSources int) (kept, skipped []ExtractedConcept) {
	if minSources <= 0 {
		return concepts, nil
	}
	for _, c := range concepts {
		if len(c.Sources) >= minSources {
			kept = append(kept, c)
		} else {
			skipped = append(skipped, c)
		}
	}
	for _, c := range skipped {
		log.Info("article skipped: no evidence", "concept", c.Name, "sources", len(c.Sources))
	}
	return kept, skipped
}

// mergeConceptIntoManifest unions an extracted concept's sources AND aliases
// into an existing manifest concept (independent dedup sets — an alias
// string equal to a source path must not be dropped). Shared by the
// embedding-dedup drop path (which never reaches AddConcept) so rule-2
// renamed concepts keep their aliases (issue #128, gates i1/QA).
func mergeConceptIntoManifest(mf *manifest.Manifest, match string, c ExtractedConcept) {
	existing, ok := mf.Concepts[match]
	if !ok {
		return
	}
	seenSrc := make(map[string]bool)
	for _, s := range existing.Sources {
		seenSrc[s] = true
	}
	for _, s := range c.Sources {
		if !seenSrc[s] {
			existing.Sources = append(existing.Sources, s)
		}
	}
	seenAlias := make(map[string]bool)
	for _, a := range existing.Aliases {
		seenAlias[a] = true
	}
	for _, a := range c.Aliases {
		if !seenAlias[a] {
			seenAlias[a] = true
			existing.Aliases = append(existing.Aliases, a)
		}
	}
	mf.Concepts[match] = existing
}
