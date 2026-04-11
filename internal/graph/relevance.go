package graph

import (
	"math"
	"sort"

	"github.com/xoai/sage-wiki/internal/ontology"
)

// RelevanceWeights configures the weight of each scoring signal.
type RelevanceWeights struct {
	DirectLink     float64
	SourceOverlap  float64
	CommonNeighbor float64
	TypeAffinity   float64
}

// DefaultWeights returns the default signal weights.
func DefaultWeights() RelevanceWeights {
	return RelevanceWeights{
		DirectLink:     3.0,
		SourceOverlap:  4.0,
		CommonNeighbor: 1.5,
		TypeAffinity:   1.0,
	}
}

// RelevanceOpts configures the graph relevance scorer.
type RelevanceOpts struct {
	SeedIDs   []string         // initial entity IDs from hybrid search
	MaxExpand int              // max additional articles to return (default 10)
	MaxDepth  int              // graph traversal depth (default 2)
	Weights   RelevanceWeights // signal weights
}

// ScoredArticle represents a candidate article with its relevance score.
type ScoredArticle struct {
	EntityID string
	Score    float64
	Signals  map[string]float64
}

// typeAffinityMatrix defines bonuses for entity type pairs.
// Diagonal (same-type) gets lower bonus to encourage cross-type discovery.
var typeAffinityMatrix = map[string]map[string]float64{
	"concept":   {"concept": 0.8, "technique": 1.2, "source": 1.0, "claim": 1.0, "artifact": 0.8},
	"technique": {"concept": 1.2, "technique": 0.8, "source": 1.0, "claim": 0.8, "artifact": 1.0},
	"source":    {"concept": 1.0, "technique": 1.0, "source": 0.5, "claim": 0.8, "artifact": 0.8},
	"claim":     {"concept": 1.0, "technique": 0.8, "source": 0.8, "claim": 0.5, "artifact": 1.0},
	"artifact":  {"concept": 0.8, "technique": 1.0, "source": 0.8, "claim": 1.0, "artifact": 0.5},
}

// getTypeAffinity returns the affinity score between two entity types.
// Returns 1.0 for unknown types.
func getTypeAffinity(typeA, typeB string) float64 {
	if row, ok := typeAffinityMatrix[typeA]; ok {
		if val, ok := row[typeB]; ok {
			return val
		}
	}
	return 1.0
}

// ScoreRelevance computes graph-based relevance scores for candidate articles
// relative to seed entities. Uses only the ontology store (no manifest needed).
//
// Four signals:
//   - direct_link:     ontology relation exists between seed and candidate (excluding cites)
//   - source_overlap:  shared cites targets (source entities) between seed and candidate
//   - common_neighbor: Adamic-Adar index — shared neighbors weighted by 1/log(degree)
//   - type_affinity:   bonus based on entity type pairs
func ScoreRelevance(ont *ontology.Store, opts RelevanceOpts) ([]ScoredArticle, error) {
	if len(opts.SeedIDs) == 0 {
		return nil, nil
	}
	if opts.MaxExpand <= 0 {
		opts.MaxExpand = 10
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 2
	}

	seedSet := make(map[string]bool, len(opts.SeedIDs))
	for _, id := range opts.SeedIDs {
		seedSet[id] = true
	}

	// Collect seed entity types for type affinity scoring
	seedTypes := make(map[string]string) // id → type
	for _, id := range opts.SeedIDs {
		e, err := ont.GetEntity(id)
		if err != nil {
			return nil, err
		}
		if e != nil {
			seedTypes[id] = e.Type
		}
	}

	// Build seed source sets for source overlap (via ontology cites relations)
	seedSources := make(map[string]map[string]bool) // seedID → set of source entity IDs
	for _, id := range opts.SeedIDs {
		cited, err := ont.CitedBy(id)
		if err != nil {
			return nil, err
		}
		srcSet := make(map[string]bool, len(cited))
		for _, c := range cited {
			srcSet[c.ID] = true
		}
		seedSources[id] = srcSet
	}

	// Candidate scoring map
	scores := make(map[string]*ScoredArticle)

	ensureCandidate := func(id string) *ScoredArticle {
		if c, ok := scores[id]; ok {
			return c
		}
		c := &ScoredArticle{
			EntityID: id,
			Signals:  make(map[string]float64),
		}
		scores[id] = c
		return c
	}

	// Signal 1: Direct link — traverse from seeds, excluding cites relations
	for _, seedID := range opts.SeedIDs {
		neighbors, err := collectNeighbors(ont, seedID, opts.MaxDepth)
		if err != nil {
			return nil, err
		}
		for _, nID := range neighbors {
			if seedSet[nID] {
				continue
			}
			c := ensureCandidate(nID)
			c.Signals["direct_link"] += 1.0 // accumulates across seeds
		}
	}

	// Signal 2: Source overlap — concepts sharing cites targets
	for _, seedID := range opts.SeedIDs {
		srcSet := seedSources[seedID]
		if len(srcSet) == 0 {
			continue
		}
		for srcID := range srcSet {
			citers, err := ont.EntitiesCiting(srcID)
			if err != nil {
				return nil, err
			}
			for _, citer := range citers {
				if seedSet[citer.ID] || citer.ArticlePath == "" {
					continue
				}
				// Co-citation: count shared source entities, normalized by total seed sources
				c := ensureCandidate(citer.ID)
				c.Signals["source_overlap"] += 1.0 // raw co-citation count, normalized below
			}
		}
	}

	// Normalize source overlap by total seed source count
	totalSeedSources := 0
	for _, s := range seedSources {
		totalSeedSources += len(s)
	}
	if totalSeedSources > 0 {
		for _, c := range scores {
			if raw, ok := c.Signals["source_overlap"]; ok && raw > 0 {
				c.Signals["source_overlap"] = raw / float64(totalSeedSources)
			}
		}
	}

	// Signal 3: Adamic-Adar — shared neighbors weighted by 1/log(degree)
	// Build seed neighbor sets (non-cites, non-source entities)
	seedNeighborSets := make(map[string]map[string]bool)
	for _, seedID := range opts.SeedIDs {
		neighbors, err := collectNeighbors(ont, seedID, 1)
		if err != nil {
			return nil, err
		}
		nSet := make(map[string]bool, len(neighbors))
		for _, n := range neighbors {
			nSet[n] = true
		}
		seedNeighborSets[seedID] = nSet
	}

	// For each candidate, compute Adamic-Adar
	for candidateID, c := range scores {
		candidateNeighbors, err := collectNeighbors(ont, candidateID, 1)
		if err != nil {
			return nil, err
		}
		var aaScore float64
		for _, cn := range candidateNeighbors {
			// Check if this neighbor is shared with any seed
			shared := false
			for _, seedID := range opts.SeedIDs {
				if seedNeighborSets[seedID][cn] {
					shared = true
					break
				}
			}
			if !shared {
				continue
			}
			deg, err := ont.EntityDegree(cn)
			if err != nil {
				return nil, err
			}
			if deg > 1 {
				aaScore += 1.0 / math.Log(float64(deg))
			}
		}
		if aaScore > 0 {
			c.Signals["common_neighbor"] = aaScore
		}
	}

	// Signal 4: Type affinity
	for candidateID, c := range scores {
		e, err := ont.GetEntity(candidateID)
		if err != nil {
			return nil, err
		}
		if e == nil {
			continue
		}
		var totalAffinity float64
		var count int
		for _, seedType := range seedTypes {
			totalAffinity += getTypeAffinity(seedType, e.Type)
			count++
		}
		if count > 0 {
			c.Signals["type_affinity"] = totalAffinity / float64(count)
		}
	}

	// Compute combined scores
	w := opts.Weights
	for _, c := range scores {
		c.Score = w.DirectLink*c.Signals["direct_link"] +
			w.SourceOverlap*c.Signals["source_overlap"] +
			w.CommonNeighbor*c.Signals["common_neighbor"] +
			w.TypeAffinity*c.Signals["type_affinity"]
	}

	// Filter: only entities with ArticlePath (exclude source entities)
	var results []ScoredArticle
	for _, c := range scores {
		e, err := ont.GetEntity(c.EntityID)
		if err != nil {
			return nil, err
		}
		if e != nil && e.ArticlePath != "" {
			results = append(results, *c)
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Cap at MaxExpand
	if len(results) > opts.MaxExpand {
		results = results[:opts.MaxExpand]
	}

	return results, nil
}

// collectNeighbors returns non-source entity IDs reachable from entityID
// via non-cites relations up to the given depth. Excludes the start entity.
func collectNeighbors(ont *ontology.Store, entityID string, maxDepth int) ([]string, error) {
	visited := map[string]bool{entityID: true}
	queue := []string{entityID}
	var neighbors []string

	for depth := 0; depth < maxDepth && len(queue) > 0; depth++ {
		var nextQueue []string
		for _, id := range queue {
			rels, err := ont.GetRelations(id, ontology.Both, "")
			if err != nil {
				return nil, err
			}
			for _, r := range rels {
				// Skip cites relations (they connect to source entities)
				if r.Relation == ontology.RelCites {
					continue
				}
				neighborID := r.TargetID
				if neighborID == id {
					neighborID = r.SourceID
				}
				if visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				nextQueue = append(nextQueue, neighborID)
				neighbors = append(neighbors, neighborID)
			}
		}
		queue = nextQueue
	}

	return neighbors, nil
}
