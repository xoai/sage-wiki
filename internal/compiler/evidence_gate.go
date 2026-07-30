package compiler

import (
	"github.com/xoai/sage-wiki/internal/log"
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
