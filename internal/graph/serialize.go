package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// SubgraphOpts bounds a serialization. The caller passes RESOLVED values —
// config defaults are applied by the query layer (applyGraphQueryDefaults),
// which is this function's only config-aware caller.
type SubgraphOpts struct {
	MaxHops  int
	MaxEdges int
	// AsOf makes the subgraph point-in-time (P3-6): only edges live at AsOf
	// are serialized. Zero means now.
	AsOf time.Time
}

// SerializedEdge is one numbered triple. Line is the BARE triple — the
// provenance braces tag lives only in Subgraph.Text, so format tests must
// compare Text, not Line.
type SerializedEdge struct {
	Index int            // 1-based; the E-number the model cites
	Line  string         // "(Buzz Aldrin) --[extends]--> (Apollo 11)"
	Rel   store.Relation // Evidence/Confidence/SourceDoc carried verbatim
}

// Subgraph is an LLM-ready serialization of a k-hop neighborhood.
type Subgraph struct {
	Edges     []SerializedEdge
	Text      string
	Truncated bool
	Seeds     []string // post-resolution canonical ids actually expanded
}

// SerializeSubgraph BFS-expands the seeds and emits numbered triple lines
// with per-edge provenance tags.
//
// Seeds resolve HERE, at the consumer boundary (store.CanonicalOrSelf) —
// store reads stay raw (D2), and the query layer deliberately does not
// resolve a second time. Edge identity for dedupe is the TRIPLE
// (SourceID, Relation, TargetID), not Rel.ID: legacy rows can reach this
// reader with COALESCE'd empty ids, and two distinct triples must both
// survive equal empty ids. Within each hop edges are sorted
// (SourceID, Relation, TargetID) BEFORE the cap applies, so both the ORDER
// and the retained SET are deterministic on every backend.
func SerializeSubgraph(ont store.OntologyStore, seeds []string, opts SubgraphOpts) (Subgraph, error) {
	resolved := make([]string, 0, len(seeds))
	seenSeed := make(map[string]bool, len(seeds))
	for _, id := range seeds {
		cid := store.CanonicalOrSelf(ont, id)
		if !seenSeed[cid] {
			seenSeed[cid] = true
			resolved = append(resolved, cid)
		}
	}

	type tripleKey struct{ src, rel, tgt string }
	collected := make(map[tripleKey]bool)
	visited := make(map[string]bool)

	var edges []SerializedEdge
	truncated := false
	frontier := resolved

	for hop := 1; hop <= opts.MaxHops && len(frontier) > 0 && !truncated; hop++ {
		var next []string
		var hopRels []store.Relation
		for _, id := range frontier {
			if visited[id] {
				continue
			}
			visited[id] = true
			rels, err := ont.GetRelationsAt(id, store.Both, "", opts.AsOf)
			if err != nil {
				return Subgraph{}, fmt.Errorf("serialize subgraph: %w", err)
			}
			for _, r := range rels {
				k := tripleKey{r.SourceID, r.Relation, r.TargetID}
				if collected[k] {
					continue
				}
				collected[k] = true
				hopRels = append(hopRels, r)
				neighbor := r.TargetID
				if neighbor == id {
					neighbor = r.SourceID
				}
				if !visited[neighbor] {
					next = append(next, neighbor)
				}
			}
		}
		// Sort BEFORE the cap: truncating during collection would make the
		// retained subset depend on backend return order.
		sort.Slice(hopRels, func(i, j int) bool {
			a, b := hopRels[i], hopRels[j]
			if a.SourceID != b.SourceID {
				return a.SourceID < b.SourceID
			}
			if a.Relation != b.Relation {
				return a.Relation < b.Relation
			}
			return a.TargetID < b.TargetID
		})
		for _, r := range hopRels {
			if len(edges) >= opts.MaxEdges {
				truncated = true
				break
			}
			edges = append(edges, SerializedEdge{Rel: r})
		}
		frontier = next
	}

	names := make(map[string]string)
	name := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := id // missing entity rows must not error; the id stands in
		if e, err := ont.GetEntity(id); err != nil {
			// A real lookup ERROR is not a missing row — the id fallback
			// still renders, but silence would conflate the two.
			log.Warn("serialize: entity lookup failed — rendering id instead of name",
				"entity", id, "error", err)
		} else if e != nil && e.Name != "" {
			n = e.Name
		}
		names[id] = n
		return n
	}

	var lines []string
	for i := range edges {
		r := edges[i].Rel
		edges[i].Index = i + 1
		edges[i].Line = fmt.Sprintf("(%s) --[%s]--> (%s)", name(r.SourceID), r.Relation, name(r.TargetID))
		lines = append(lines, fmt.Sprintf("E%d: %s%s", i+1, edges[i].Line, provenanceTag(r)))
	}

	return Subgraph{
		Edges:     edges,
		Text:      strings.Join(lines, "\n"),
		Truncated: truncated,
		Seeds:     resolved,
	}, nil
}

// provenanceTag renders the compact per-edge provenance. Confidence 0 means
// "not scored" (Pass-3 keyword edges) — printing 0.00 would read as a claim,
// so the field is omitted; likewise an empty source. All three empty → no
// tag at all.
func provenanceTag(r store.Relation) string {
	var parts []string
	if r.SourceDoc != "" {
		parts = append(parts, "source: "+r.SourceDoc)
	}
	if r.Confidence > 0 {
		parts = append(parts, fmt.Sprintf("confidence: %.2f", r.Confidence))
	}
	if r.Evidence != "" {
		parts = append(parts, fmt.Sprintf("evidence: %q", "«"+r.Evidence+"»"))
	}
	// P3-6: the validity window travels with the edge (GRAPH-08). "now" for
	// an open window — the edge is currently valid.
	if r.ValidFrom != "" || r.ValidTo != "" {
		from, to := r.ValidFrom, r.ValidTo
		if from == "" {
			from = "…"
		}
		if to == "" {
			to = "now"
		}
		parts = append(parts, fmt.Sprintf("valid: %s→%s", from, to))
	}
	if len(parts) == 0 {
		return ""
	}
	return " {" + strings.Join(parts, ", ") + "}"
}
