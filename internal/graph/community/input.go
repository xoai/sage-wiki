package community

import (
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// BuildInput assembles the detection graph from the ontology store (P3-5):
// live semantic edges between non-source entities. One batch entity load —
// the web-view precedent, never N+1 GetEntity per edge. Liveness uses
// ontology.LiveAt regardless of the temporal.enabled read-path gate:
// communities are themselves an answer path.
func BuildInput(ont store.OntologyStore) (nodes []string, edges []Edge, err error) {
	entities, err := ont.ListEntities("")
	if err != nil {
		return nil, nil, fmt.Errorf("community.BuildInput: list entities: %w", err)
	}
	types := make(map[string]string, len(entities))
	for _, e := range entities {
		types[e.ID] = e.Type
	}

	rels, err := ont.AllRelations()
	if err != nil {
		return nil, nil, fmt.Errorf("community.BuildInput: all relations: %w", err)
	}

	now := time.Now().UTC()
	nodeSet := map[string]bool{}
	for _, r := range rels {
		if r.Relation == "cites" || r.SourceID == r.TargetID {
			continue // document links / self-loops: no community signal
		}
		st, sok := types[r.SourceID]
		tt, tok := types[r.TargetID]
		if !sok || !tok || st == "source" || tt == "source" {
			continue
		}
		if !ontology.LiveAt(r, now) {
			continue // P3-6: communities reflect the live graph
		}
		edges = append(edges, Edge{From: r.SourceID, To: r.TargetID})
		nodeSet[r.SourceID] = true
		nodeSet[r.TargetID] = true
	}

	// Isolated entities are excluded: no edges, no community signal.
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	return nodes, edges, nil
}
