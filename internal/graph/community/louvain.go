// Package community implements pure-Go Louvain community detection for the
// ontology graph (P3-5). No external dependencies, no RNG: every iteration
// runs over sorted slices, so detection is byte-for-byte deterministic —
// that is a tested contract, not a hope.
package community

import (
	"sort"
	"strconv"

	"github.com/xoai/sage-wiki/internal/store"
)

// Edge is one undirected, unweighted edge between two entity IDs.
type Edge struct {
	From, To string
}

// Level is one hierarchy tier: Communities holds the partition at that
// level as flattened sets of ORIGINAL node IDs, each sorted, the slice
// ordered by each community's minimum member ID (the pinned seq order —
// community IDs c<level>-<seq> are assigned over this order).
type Level struct {
	Communities [][]string
}

const (
	moveEpsilon  = 1e-10 // minimum modularity gain for a node move
	levelEpsilon = 1e-5  // minimum per-level modularity gain to keep aggregating
	maxLevelsCap = 4
)

// MemberHash delegates to store.MemberHash — THE shared member-set hash
// (it lives in store because the store layer itself uses it for the
// conditional summary clear; keeping it here would import-cycle).
var MemberHash = store.MemberHash

// quotient is a weighted graph (Louvain aggregation levels need weights;
// level 0 has all weights 1).
type quotient struct {
	nodes []string // node IDs (community keys at level >0)
	adj   map[string]map[string]float64
	deg   map[string]float64  // self-loops counted twice
	mem   map[string][]string // node -> flattened original member IDs
	m     float64             // total degree weight
}

func buildQuotient(nodes []string, edges []Edge) *quotient {
	q := &quotient{
		adj: make(map[string]map[string]float64, len(nodes)),
		deg: make(map[string]float64, len(nodes)),
		mem: make(map[string][]string, len(nodes)),
	}
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if seen[n] {
			continue
		}
		seen[n] = true
		q.nodes = append(q.nodes, n)
		q.adj[n] = map[string]float64{}
		q.mem[n] = []string{n}
	}
	sort.Strings(q.nodes)
	// Duplicate edges accumulate (the aggregated levels rely on it).
	for _, e := range edges {
		if e.From == e.To || !seen[e.From] || !seen[e.To] {
			continue
		}
		q.adj[e.From][e.To]++
		q.adj[e.To][e.From]++
		q.deg[e.From]++
		q.deg[e.To]++
		q.m += 2
	}
	return q
}

// aggregate collapses each community into one node keyed
// "c:<index>" (index into the sorted community slice). Internal weight
// becomes self-loops (counted twice, preserving degree sums).
func (q *quotient) aggregate(comms [][]string) *quotient {
	idxOf := make(map[string]int, len(q.nodes))
	for i, c := range comms {
		for _, n := range c {
			idxOf[n] = i
		}
	}
	nq := &quotient{
		adj: make(map[string]map[string]float64, len(comms)),
		deg: make(map[string]float64, len(comms)),
		mem: make(map[string][]string, len(comms)),
	}
	for i := range comms {
		id := commKeyOf(i)
		nq.adj[id] = map[string]float64{}
		nq.mem[id] = append([]string(nil), comms[i]...)
	}
	for _, u := range q.nodes {
		cu := commKeyOf(idxOf[u])
		for _, v := range sortedKeys(q.adj[u]) {
			cv := commKeyOf(idxOf[v])
			nq.adj[cu][cv] += q.adj[u][v]
			nq.deg[cu] += q.adj[u][v]
			nq.m += q.adj[u][v]
		}
	}
	return nq
}

func commKeyOf(i int) string {
	return "c:" + itoa(i)
}

var itoa = strconv.Itoa

// localMoving runs the Louvain local-moving phase: move each node to the
// neighboring community with the best strictly-positive modularity gain,
// in sorted node order, ties broken by sorted community key order, until no
// move improves. Returns the partition (communities of quotient node IDs,
// each sorted, ordered by min member ID) and the modularity.
func (q *quotient) localMoving() ([][]string, float64) {
	commOf := make(map[string]string, len(q.nodes)) // node -> community key
	tot := make(map[string]float64, len(q.nodes))   // community -> degree sum
	for _, n := range q.nodes {
		commOf[n] = n // singleton start
		tot[n] = q.deg[n]
	}

	moved := true
	for moved {
		moved = false
		for _, n := range q.nodes {
			own := commOf[n]
			ki := q.deg[n]
			// Remove n from its community.
			tot[own] -= ki
			// Gain per neighboring community (weights grouped by community).
			// The self-loop is EXCLUDED: it is internal to every candidate
			// (it cancels in exact ΔQ math), so counting it inflates only
			// the stay side and over-anchors super-nodes at aggregation
			// levels ≥ 1 (independent review). python-louvain's remove_cost
			// constant achieves the same exclusion.
			byComm := map[string]float64{}
			for _, v := range sortedKeys(q.adj[n]) {
				if v == n {
					continue
				}
				byComm[commOf[v]] += q.adj[n][v]
			}
			comms := make([]string, 0, len(byComm)+1)
			for c := range byComm {
				comms = append(comms, c)
			}
			if _, ok := byComm[own]; !ok {
				comms = append(comms, own) // staying is a candidate
			}
			sort.Strings(comms)

			best, bestGain := own, 0.0
			for _, c := range comms {
				gain := byComm[c] - tot[c]*ki/q.m
				if gain > bestGain+moveEpsilon {
					best, bestGain = c, gain
				}
			}
			tot[best] += ki
			if best != own {
				commOf[n] = best
				moved = true
			}
		}
	}

	// Collect and order the partition.
	byComm := map[string][]string{}
	for _, n := range q.nodes {
		c := commOf[n]
		byComm[c] = append(byComm[c], n)
	}
	comms := make([][]string, 0, len(byComm))
	for _, members := range byComm {
		sort.Strings(members)
		comms = append(comms, members)
	}
	sortByMinMember(comms)
	return comms, q.modularity(commOf)
}

func (q *quotient) modularity(commOf map[string]string) float64 {
	if q.m == 0 {
		return 0
	}
	inW := map[string]float64{} // internal ordered-pair weight
	tot := map[string]float64{} // community degree sums
	for _, u := range q.nodes {
		cu := commOf[u]
		tot[cu] += q.deg[u]
		for _, v := range sortedKeys(q.adj[u]) {
			if commOf[v] == cu {
				inW[cu] += q.adj[u][v]
			}
		}
	}
	var qq float64
	// Iterate community keys in sorted order: float addition is not
	// associative, and the level-stop compares against an epsilon — map
	// order must never decide whether the hierarchy continues.
	keys := make([]string, 0, len(tot))
	for c := range tot {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		qq += inW[c]/q.m - (tot[c]/q.m)*(tot[c]/q.m)
	}
	return qq
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortByMinMember orders communities by their minimum member ID
// (lexicographic) — the pinned seq-assignment order.
func sortByMinMember(comms [][]string) {
	sort.Slice(comms, func(i, j int) bool {
		return comms[i][0] < comms[j][0]
	})
}

// Detect runs hierarchical Louvain: level 0 is the base partition; each
// further level re-runs detection over the quotient of the level below,
// stopping at a single community, when the level's modularity gain is below
// levelEpsilon, or at min(maxLevels, 4) levels. Communities at every level
// are flattened to original node IDs.
func Detect(nodes []string, edges []Edge, maxLevels int) []Level {
	if maxLevels <= 0 {
		return nil
	}
	if maxLevels > maxLevelsCap {
		maxLevels = maxLevelsCap
	}
	q := buildQuotient(nodes, edges)
	if len(q.nodes) == 0 {
		return nil
	}

	var levels []Level
	prevQ := 0.0
	for len(levels) < maxLevels {
		comms, mod := q.localMoving()
		if len(levels) > 0 && (len(comms) == 1 || mod-prevQ < levelEpsilon) {
			// A merged-to-one or non-improving top level is not kept — it
			// would be a near-duplicate of the level below that pickLevel
			// prefers and that gets summarized for LLM cost.
			break
		}
		flat := make([][]string, len(comms))
		for i, c := range comms {
			var members []string
			for _, key := range c {
				members = append(members, q.mem[key]...)
			}
			sort.Strings(members)
			flat[i] = members
		}
		sortByMinMember(flat)
		levels = append(levels, Level{Communities: flat})
		prevQ = mod
		q = q.aggregate(comms)
	}
	return levels
}
