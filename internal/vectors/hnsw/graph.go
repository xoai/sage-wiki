package hnsw

import (
	"cmp"
	"fmt"
	"math"
	"math/rand"
	"time"
)

type Vector = []float32

// Node is a node in the graph.
type Node[K cmp.Ordered] struct {
	Key   K
	Value Vector
}

func MakeNode[K cmp.Ordered](key K, vec Vector) Node[K] {
	return Node[K]{Key: key, Value: vec}
}

// layerNode is a node in a layer of the graph.
type layerNode[K cmp.Ordered] struct {
	Node[K]

	// neighbors is map of neighbor keys to neighbor nodes.
	// It is a map and not a slice to allow for efficient deletes, esp.
	// when M is high.
	neighbors map[K]*layerNode[K]
}

// addNeighbor adds a o neighbor to the node, replacing the neighbor
// with the worst distance if the neighbor set is full.
func (n *layerNode[K]) addNeighbor(newNode *layerNode[K], m int, dist DistanceFunc) {
	if n.neighbors == nil {
		n.neighbors = make(map[K]*layerNode[K], m)
	}

	n.neighbors[newNode.Key] = newNode
	if len(n.neighbors) <= m {
		return
	}

	// Find the neighbor with the worst distance.
	var (
		worstDist = float32(math.Inf(-1))
		worst     *layerNode[K]
	)
	for _, neighbor := range n.neighbors {
		d := dist(neighbor.Value, n.Value)
		// d > worstDist may always be false if the distance function
		// returns NaN, e.g., when the embeddings are zero.
		if d > worstDist || worst == nil {
			worstDist = d
			worst = neighbor
		}
	}

	delete(n.neighbors, worst.Key)
	// Delete backlink from the worst neighbor.
	delete(worst.neighbors, n.Key)
	worst.replenish(m)
}

type searchCandidate[K cmp.Ordered] struct {
	node *layerNode[K]
	dist float32
}

func (s searchCandidate[K]) Less(o searchCandidate[K]) bool {
	return s.dist < o.dist
}

// search returns the k nearest layer nodes to target, sorted ascending by
// distance. This is textbook HNSW ef-search (Malkov & Yashunin 2014,
// Algorithm 2): a min-heap frontier, a sorted result list capped at ef,
// terminating when the nearest frontier candidate is farther than the
// worst kept result.
//
// VENDORED REWRITE (sage-wiki P2-7): upstream's version was greedy
// hill-climbing — it terminated when no neighbor beat the CURRENT BEST
// result, so exploration stopped at the first local optimum (~60% of
// self-queries failed on a 2000x64-dim corpus; layer structure was
// healthy, the descent never reached the target). It also poisoned
// construction, since Add builds neighborhoods with this same search.
// Upstream's min-heap Max()/PopLast() eviction bug (Max() returns an
// arbitrary leaf, not the maximum) is moot under the rewrite.
func (n *layerNode[K]) search(
	// k is the number of candidates in the result set.
	k int,
	efSearch int,
	target Vector,
	distance DistanceFunc,
) []searchCandidate[K] {
	ef := efSearch
	if ef < k {
		ef = k
	}

	entry := searchCandidate[K]{node: n, dist: distance(n.Value, target)}
	candidates := Heap[searchCandidate[K]]{}
	candidates.Init(make([]searchCandidate[K], 0, efSearch))
	candidates.Push(entry)

	// result stays sorted ascending by dist, capped at ef.
	result := []searchCandidate[K]{entry}
	visited := map[K]bool{n.Key: true}

	for candidates.Len() > 0 {
		current := candidates.Pop()
		if len(result) >= ef && current.dist > result[len(result)-1].dist {
			break // nearest frontier node is worse than the worst kept result
		}
		for _, neighbor := range current.node.neighbors {
			if visited[neighbor.Key] {
				continue
			}
			visited[neighbor.Key] = true
			d := distance(neighbor.Value, target)
			if len(result) >= ef && d >= result[len(result)-1].dist {
				continue
			}
			pos := 0
			for pos < len(result) && result[pos].dist <= d {
				pos++
			}
			result = append(result, searchCandidate[K]{})
			copy(result[pos+1:], result[pos:])
			result[pos] = searchCandidate[K]{node: neighbor, dist: d}
			candidates.Push(searchCandidate[K]{node: neighbor, dist: d})
			if len(result) > ef {
				result = result[:ef]
			}
		}
	}

	if len(result) > k {
		result = result[:k]
	}
	return result
}

func (n *layerNode[K]) replenish(m int) {
	if len(n.neighbors) >= m {
		return
	}

	// Restore connectivity by adding new neighbors.
	// This is a naive implementation that could be improved by
	// using a priority queue to find the best candidates.
	for _, neighbor := range n.neighbors {
		for key, candidate := range neighbor.neighbors {
			if _, ok := n.neighbors[key]; ok {
				// do not add duplicates
				continue
			}
			if candidate == n {
				continue
			}
			n.addNeighbor(candidate, m, CosineDistance)
			if len(n.neighbors) >= m {
				return
			}
		}
	}
}

// isolates remove the node from the graph by removing all connections
// to neighbors.
func (n *layerNode[K]) isolate(m int) {
	// Vendored fix: TWO passes. Single-pass isolate+replenish resurrected
	// the deleted node as a GHOST: replenish(n₁) walked n₂'s neighbor list
	// — which still contained the deleted node, since n₂'s cleanup hadn't
	// run yet — and re-added it. The ghost then surfaced in searches
	// (deleted key returned as a hit, nil-deref on elevator lookup).
	for _, neighbor := range n.neighbors {
		delete(neighbor.neighbors, n.Key)
	}
	for _, neighbor := range n.neighbors {
		neighbor.replenish(m)
	}
}

type layer[K cmp.Ordered] struct {
	// nodes is a map of nodes IDs to nodes.
	// All nodes in a higher layer are also in the lower layers, an essential
	// property of the graph.
	//
	// nodes is exported for interop with encoding/gob.
	nodes map[K]*layerNode[K]
}

// entry returns the entry node of the layer.
// It doesn't matter which node is returned, even that the
// entry node is consistent, so we just return the first node
// in the map to avoid tracking extra state.
func (l *layer[K]) entry() *layerNode[K] {
	if l == nil {
		return nil
	}
	for _, node := range l.nodes {
		return node
	}
	return nil
}

func (l *layer[K]) size() int {
	if l == nil {
		return 0
	}
	return len(l.nodes)
}

// Graph is a Hierarchical Navigable Small World graph.
// All public parameters must be set before adding nodes to the graph.
// K is cmp.Ordered instead of of comparable so that they can be sorted.
type Graph[K cmp.Ordered] struct {
	// Distance is the distance function used to compare embeddings.
	Distance DistanceFunc

	// Rng is used for level generation. It may be set to a deterministic value
	// for reproducibility. Note that deterministic number generation can lead to
	// degenerate graphs when exposed to adversarial inputs.
	Rng *rand.Rand

	// M is the maximum number of neighbors to keep for each node.
	// A good default for OpenAI embeddings is 16.
	M int

	// Ml is the level generation factor.
	// E.g., for Ml = 0.25, each layer is 1/4 the size of the previous layer.
	Ml float64

	// EfSearch is the number of nodes to consider in the search phase.
	// 20 is a reasonable default. Higher values improve search accuracy at
	// the expense of memory.
	EfSearch int

	// layers is a slice of layers in the graph.
	layers []*layer[K]
}

func defaultRand() *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

// NewGraph returns a new graph with default parameters, roughly designed for
// storing OpenAI embeddings.
func NewGraph[K cmp.Ordered]() *Graph[K] {
	return &Graph[K]{
		M:        16,
		Ml:       0.25,
		Distance: CosineDistance,
		EfSearch: 20,
		Rng:      defaultRand(),
	}
}

// maxLevel returns an upper-bound on the number of levels in the graph
// based on the size of the base layer.
func maxLevel(ml float64, numNodes int) int {
	if ml == 0 {
		panic("ml must be greater than 0")
	}

	if numNodes == 0 {
		return 1
	}

	l := math.Log(float64(numNodes))
	l /= math.Log(1 / ml)

	m := int(math.Round(l)) + 1

	return m
}

// randomLevel generates a random level for a new node.
func (h *Graph[K]) randomLevel() int {
	// max avoids having to accept an additional parameter for the maximum level
	// by calculating a probably good one from the size of the base layer.
	max := 1
	if len(h.layers) > 0 {
		if h.Ml == 0 {
			panic("(*Graph).Ml must be greater than 0")
		}
		max = maxLevel(h.Ml, h.layers[0].size())
	}

	for level := 0; level < max; level++ {
		if h.Rng == nil {
			h.Rng = defaultRand()
		}
		r := h.Rng.Float64()
		if r > h.Ml {
			return level
		}
	}

	return max
}

func (g *Graph[K]) assertDims(n Vector) {
	hasDims := g.Dims()
	if hasDims == 0 {
		return // empty graph (or freshly pruned) — dims set by the next insert
	}
	if hasDims != len(n) {
		panic(fmt.Sprint("embedding dimension mismatch: ", hasDims, " != ", len(n)))
	}
}

// Dims returns the number of dimensions in the graph, or
// 0 if the graph is empty. Nil-tolerant for a pruned base layer
// (vendored fix: Delete may leave the base empty until the next Add).
func (g *Graph[K]) Dims() int {
	if len(g.layers) == 0 {
		return 0
	}
	e := g.layers[0].entry()
	if e == nil {
		return 0
	}
	return len(e.Value)
}

func ptr[T any](v T) *T {
	return &v
}

// Add inserts nodes into the graph.
// If another node with the same ID exists, it is replaced.
func (g *Graph[K]) Add(nodes ...Node[K]) {
	for _, node := range nodes {
		key := node.Key
		vec := node.Value

		g.assertDims(vec)
		// Replace-on-duplicate (vendored fix): delete the old key FIRST —
		// deleting mid-loop invalidates the elevator (a nil searchPoint
		// dereference) and the final invariant check (Len unchanged on
		// replace → panic "node not added").
		replacing := g.Delete(key)
		insertLevel := g.randomLevel()
		// Create layers that don't exist yet.
		for insertLevel >= len(g.layers) {
			g.layers = append(g.layers, &layer[K]{})
		}

		if insertLevel < 0 {
			panic("invalid level")
		}

		var elevator *K

		preLen := g.Len()

		// Insert node at each layer, beginning with the highest.
		for i := len(g.layers) - 1; i >= 0; i-- {
			layer := g.layers[i]
			newNode := &layerNode[K]{
				Node: Node[K]{
					Key:   key,
					Value: vec,
				},
			}

			// Insert the new node into the layer.
			if layer.entry() == nil {
				layer.nodes = map[K]*layerNode[K]{key: newNode}
				continue
			}

			// Now at the highest layer with more than one node, so we can begin
			// searching for the best way to enter the graph.
			searchPoint := layer.entry()

			// On subsequent layers, we use the elevator node to enter the graph
			// at the best point.
			if elevator != nil {
				searchPoint = layer.nodes[*elevator]
			}

			if g.Distance == nil {
				panic("(*Graph).Distance must be set")
			}

			neighborhood := searchPoint.search(g.M, g.EfSearch, vec, g.Distance)
			if len(neighborhood) == 0 {
				// This should never happen because the searchPoint itself
				// should be in the result set.
				panic("no nodes found")
			}

			// Re-set the elevator node for the next layer.
			elevator = ptr(neighborhood[0].node.Key)

			if insertLevel >= i {
				// Insert the new node into the layer.
				layer.nodes[key] = newNode
				for _, node := range neighborhood {
					// Create a bi-directional edge between the new node and the best node.
					node.node.addNeighbor(newNode, g.M, g.Distance)
					newNode.addNeighbor(node.node, g.M, g.Distance)
				}
			}
		}

		// Invariant check: the node should have been added to the graph
		// (on replace the count stays the same by design).
		want := preLen + 1
		if replacing {
			want = preLen
		}
		if g.Len() != want {
			panic("node not added")
		}
	}
}

// Search finds the k nearest neighbors from the target node.
func (h *Graph[K]) Search(near Vector, k int) []Node[K] {
	h.assertDims(near)
	if len(h.layers) == 0 {
		return nil
	}

	var (
		efSearch = h.EfSearch

		elevator *K
	)

	for layer := len(h.layers) - 1; layer >= 0; layer-- {
		searchPoint := h.layers[layer].entry()
		if searchPoint == nil {
			continue // vendored fix: skip empty layers (defensive; Delete prunes)
		}
		if elevator != nil {
			searchPoint = h.layers[layer].nodes[*elevator]
			if searchPoint == nil {
				// Defensive parity with the entry() guard: the
				// exists-in-every-lower-layer invariant holds today, but a
				// violated invariant must degrade, not crash.
				searchPoint = h.layers[layer].entry()
			}
		}

		// Descending hierarchies: greedy ef=1 per Malkov & Yashunin — a
		// full ef-search per upper layer multiplies distance computations
		// by ef for no recall gain (gate-review finding).
		if layer > 0 {
			nodes := searchPoint.search(1, 1, near, h.Distance)
			elevator = ptr(nodes[0].node.Key)
			continue
		}

		nodes := searchPoint.search(k, efSearch, near, h.Distance)
		out := make([]Node[K], 0, len(nodes))

		for _, node := range nodes {
			out = append(out, node.node.Node)
		}

		return out
	}

	panic("unreachable")
}

// Len returns the number of nodes in the graph.
func (h *Graph[K]) Len() int {
	if len(h.layers) == 0 {
		return 0
	}
	return h.layers[0].size()
}

// Delete removes a node from the graph by key.
// It tries to preserve the clustering properties of the graph by
// replenishing connectivity in the affected neighborhoods.
func (h *Graph[K]) Delete(key K) bool {
	if len(h.layers) == 0 {
		return false
	}

	var deleted bool
	for _, layer := range h.layers {
		node, ok := layer.nodes[key]
		if !ok {
			continue
		}
		delete(layer.nodes, key)
		node.isolate(h.M)
		deleted = true
	}

	// Vendored fix: GHOST SWEEP in two passes. replenish() creates ONE-WAY
	// neighbor links, so isolate() alone cannot remove every reference to
	// the deleted node. And the sweep itself must unlink EVERYWHERE before
	// any replenish runs — a delete+replenish single pass resurrects the
	// deleted key through not-yet-swept neighbors (the ghost then surfaces
	// in searches: deleted key returned as a hit, nil-deref elevator).
	// Deletes are rare; a full sweep is cheap and total.
	var affected []*layerNode[K]
	for _, layer := range h.layers {
		for _, node := range layer.nodes {
			if _, had := node.neighbors[key]; had {
				delete(node.neighbors, key)
				affected = append(affected, node)
			}
		}
	}
	for _, node := range affected {
		node.replenish(h.M)
	}

	// Vendored fix: prune empty layers from the top — a stale empty layer
	// panics later Adds (entry() nil deref) and Searches. Only trailing
	// layers can be empty (a node exists in every layer below its level).
	for len(h.layers) > 0 && h.layers[len(h.layers)-1].size() == 0 {
		h.layers = h.layers[:len(h.layers)-1]
	}

	return deleted
}

// Lookup returns the vector with the given key.
func (h *Graph[K]) Lookup(key K) (Vector, bool) {
	if len(h.layers) == 0 {
		return nil, false
	}

	node, ok := h.layers[0].nodes[key]
	if !ok {
		return nil, false
	}
	return node.Value, ok
}

// DebugBaseDegree reports whether key is in the base layer and its degree
// there (vendored, for structure probes).
func (h *Graph[K]) DebugBaseDegree(key K) (present bool, degree int) {
	if len(h.layers) == 0 {
		return false, 0
	}
	n, ok := h.layers[0].nodes[key]
	if !ok {
		return false, 0
	}
	return true, len(n.neighbors)
}

// DebugInvariantViolations returns keys present in layer i but missing
// from layer i-1 (vendored, for structure probes).
func (h *Graph[K]) DebugInvariantViolations() []K {
	var out []K
	for i := 1; i < len(h.layers); i++ {
		for key := range h.layers[i].nodes {
			if _, ok := h.layers[i-1].nodes[key]; !ok {
				out = append(out, key)
			}
		}
	}
	return out
}

// DebugLayerSizes returns per-layer node counts, top layer last.
func (h *Graph[K]) DebugLayerSizes() []int {
	out := make([]int, len(h.layers))
	for i, l := range h.layers {
		out[i] = l.size()
	}
	return out
}
