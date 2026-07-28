package search

import (
	"sort"
	"strings"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/store"
)

// graphLegCapFactor bounds the graph leg at limit × this factor (ADR-037).
const graphLegCapFactor = 5

// defaultRelationWeights is the per-relation-type weight curve over
// sage-wiki's built-in vocabulary (ADR-037): rare-and-informative edges up,
// high-volume citation-grade edges down, everything else 1.0.
// Config-extensible types default to 1.0 unless overridden.
var defaultRelationWeights = map[string]float64{
	"contradicts": 1.1,
	"cites":       0.7,
}

// relationWeight resolves a relation type's weight: override > default
// curve > 1.0.
func relationWeight(rel string, overrides map[string]float64) float64 {
	if overrides != nil {
		if w, ok := overrides[rel]; ok && w > 0 {
			return w
		}
	}
	if w, ok := defaultRelationWeights[rel]; ok {
		return w
	}
	return 1.0
}

// buildGraphLeg produces the graph channel's ranked list (ADR-037):
// query tokens (plus hyphenated bigrams/trigrams) seed entities via exact
// id, applied-alias chain, and active-alias lookup; BFS depth ≤2 over
// relations scores each entity distance/weight (ascending = better; the
// weight is the edge that reached it, best path kept); entities without an
// ArticlePath never surface; the list is capped at limit×5.
//
// The second return maps docID → matched alias for alias-union seeds —
// the advisory alias_of annotation (spec §2.6).
func buildGraphLeg(ont store.OntologyStore, query string, limit int, weightOverrides map[string]float64) (legList, map[string]string) {
	leg := legList{channel: ChannelGraph}
	aliases := make(map[string]string)
	if ont == nil {
		return leg, aliases
	}

	// Seed candidates: single tokens plus hyphen-joined bigrams/trigrams
	// (entity IDs are hyphenated slugs: "self attention" → self-attention).
	tokens := memory.BuildFTSTerms(strings.ToLower(query))
	seen := make(map[string]bool)
	var candidates []string
	add := func(c string) {
		if c != "" && !seen[c] {
			seen[c] = true
			candidates = append(candidates, c)
		}
	}
	for i, tok := range tokens {
		add(tok)
		if i+1 < len(tokens) {
			add(tokens[i] + "-" + tokens[i+1])
		}
		if i+2 < len(tokens) {
			add(tokens[i] + "-" + tokens[i+1] + "-" + tokens[i+2])
		}
	}

	type node struct {
		id    string
		dist  int
		score float64
	}
	best := make(map[string]*node)
	var frontier []string

	seed := func(id, viaAlias string) {
		if _, ok := best[id]; ok {
			return
		}
		if e, err := ont.GetEntity(id); err != nil || e == nil {
			return
		}
		best[id] = &node{id: id, dist: 0, score: 0}
		frontier = append(frontier, id)
		if viaAlias != "" {
			aliases["concept:"+id] = viaAlias
		}
	}

	for _, cand := range candidates {
		// Exact id (through the applied-alias chain).
		resolved := store.CanonicalOrSelf(ont, cand)
		if resolved != cand {
			seed(resolved, cand)
		} else {
			seed(cand, "")
		}
		// Active alias row (alias string → canonical id).
		if al, err := ont.GetActiveAlias(cand); err == nil && al != nil {
			seed(al.CanonicalID, cand)
		}
	}
	if len(best) == 0 {
		return leg, aliases
	}

	// BFS to depth 2; best (lowest) score per entity wins.
	for depth := 1; depth <= 2; depth++ {
		var next []string
		for _, id := range frontier {
			rels, err := ont.GetRelations(id, store.Both, "")
			if err != nil {
				continue
			}
			from := best[id]
			for _, r := range rels {
				other := r.TargetID
				if other == id {
					other = r.SourceID
				}
				w := relationWeight(r.Relation, weightOverrides)
				score := from.score + 1.0/w
				if b, ok := best[other]; !ok {
					if e, err := ont.GetEntity(other); err != nil || e == nil {
						continue
					}
					best[other] = &node{id: other, dist: depth, score: score}
					next = append(next, other)
				} else if score < b.score {
					b.score = score
				}
			}
		}
		frontier = next
	}

	// Rank ascending by score (deterministic tiebreak on id), articles only.
	nodes := make([]*node, 0, len(best))
	for _, n := range best {
		nodes = append(nodes, n)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].score != nodes[j].score {
			return nodes[i].score < nodes[j].score
		}
		return nodes[i].id < nodes[j].id
	})
	cap := limit * graphLegCapFactor
	for _, n := range nodes {
		if len(leg.hits) >= cap {
			break
		}
		e, err := ont.GetEntity(n.id)
		if err != nil || e == nil || e.ArticlePath == "" {
			continue
		}
		leg.hits = append(leg.hits, legHit{docID: "concept:" + n.id})
	}
	return leg, aliases
}
