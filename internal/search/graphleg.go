package search

import (
	"sort"
	"strings"

	"github.com/xoai/sage-wiki/internal/log"
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
// curve > 1.0. An override of 0 (or negative) EXCLUDES the relation from
// traversal — the same semantics the 4-signal scorer applies to cites
// edges (F-075: silence was the only alternative, and users need an
// off-switch per relation type).
func relationWeight(rel string, overrides map[string]float64) float64 {
	if overrides != nil {
		if w, ok := overrides[rel]; ok {
			if w <= 0 {
				return 0 // excluded
			}
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
// id resolved through the APPLIED-alias chain (pending proposals never
// influence ranking — F-074); settled-order expansion to hop ≤2 scores
// each entity by accumulated 1/weight (ascending = better, true shortest
// path); entities without an ArticlePath never surface; capped at limit×5.
//
// The second return maps docID → matched alias for alias-union seeds —
// the advisory alias_of annotation (spec §2.6).
func buildGraphLeg(ont store.OntologyStore, query string, limit int, weightOverrides map[string]float64) (legList, map[string]string) {
	entityErrLogged := false
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
		score float64
	}
	best := make(map[string]*node)

	// One traversal asks for the same entity up to three times (seed,
	// discovery, final ranking), and entity lookups were ~85% of this
	// leg's cost in the V-M5c profile. Memoize per call — the ontology
	// cannot change underneath a single search.
	entities := make(map[string]*store.Entity)
	getEntity := func(id string) *store.Entity {
		if e, ok := entities[id]; ok {
			return e
		}
		e, err := ont.GetEntity(id)
		if err != nil {
			// A store error is not "absent": log once per traversal so a
			// degraded ontology is visible rather than a quietly empty leg.
			if !entityErrLogged {
				entityErrLogged = true
				log.Warn("graph channel: entity lookup failed — graph results may be incomplete",
					"entity", id, "error", err)
			}
			e = nil
		}
		entities[id] = e
		return e
	}

	seed := func(id, viaAlias string) {
		if _, ok := best[id]; ok {
			return
		}
		if e := getEntity(id); e == nil {
			return
		}
		best[id] = &node{id: id, score: 0}
		if viaAlias != "" {
			aliases["concept:"+id] = viaAlias
		}
	}

	for _, cand := range candidates {
		// Exact id, resolved through the APPLIED-alias chain only —
		// CanonicalOrSelf covers chains to the terminal; pending rows are
		// un-approved proposals and must never influence ranking (F-074).
		resolved := store.CanonicalOrSelf(ont, cand)
		if resolved != cand {
			seed(resolved, cand)
		} else {
			seed(cand, "")
		}
	}
	if len(best) == 0 {
		return leg, aliases
	}

	// Layered Bellman-Ford, 2 rounds (F-073): layer[h][v] = the cheapest
	// score over paths of AT MOST h edges. Each round relaxes every edge
	// out of the previous layer's snapshot, so the result is exact for
	// the ≤2-edge bound and independent of relation insertion order —
	// a plain per-depth BFS propagated stale scores (proven by the
	// review's executed witness), and plain Dijkstra cannot honor the
	// hop bound when a node's cheapest score needs more hops than the
	// cheapest PATH THROUGH it (state is (node, hops), not node).
	prev := make(map[string]float64, len(best))
	changed := make(map[string]bool, len(best))
	for id, n := range best {
		prev[id] = n.score
		changed[id] = true
	}
	for hop := 1; hop <= 2; hop++ {
		next := make(map[string]float64, len(prev))
		for id, s := range prev {
			next[id] = s // ≤h includes ≤h-1 paths
		}
		nextChanged := make(map[string]bool)
		// Only nodes whose score changed last round can improve a
		// neighbor this round (NEW-2: re-relaxing unchanged seeds only
		// generates no-op sweeps).
		for id := range changed {
			s := prev[id]
			rels, err := ont.GetRelations(id, store.Both, "")
			if err != nil {
				continue
			}
			for _, r := range rels {
				other := r.TargetID
				if other == id {
					other = r.SourceID
				}
				w := relationWeight(r.Relation, weightOverrides)
				if w <= 0 {
					continue // relation excluded by config (F-075)
				}
				cand := s + 1.0/w
				cur, known := next[other]
				if !known || cand < cur {
					if _, tracked := best[other]; !tracked {
						if e := getEntity(other); e == nil {
							continue
						}
						best[other] = &node{id: other, score: cand}
					}
					next[other] = cand
					nextChanged[other] = true
				}
			}
		}
		prev = next
		changed = nextChanged
	}
	// prev now holds the exact ≤2-edge score for every discovered node.
	for id, s := range prev {
		if n, ok := best[id]; ok {
			n.score = s
		}
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
		e := getEntity(n.id)
		if e == nil || e.ArticlePath == "" {
			continue
		}
		leg.hits = append(leg.hits, legHit{docID: "concept:" + n.id})
	}
	if len(leg.hits) > 0 {
		log.Debug("graph channel", "seeds", len(candidates), "entities", len(best), "hits", len(leg.hits), "alias_seeds", len(aliases))
	}
	return leg, aliases
}
