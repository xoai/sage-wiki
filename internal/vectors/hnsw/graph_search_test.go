package hnsw

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestSearch_SelfQueryRecall is the vendored-code gate promised in
// VENDORED.md: across 20 independently seeded graphs, every self-query
// (query = a corpus vector) must return that vector as top-1. Upstream
// v0.6.1 failed ~60% of these (hill-climbing search + min-heap Max()
// fallacy); the vendored ef-search rewrite must keep this at zero
// failures. If upstream is ever restored, this test decides.
func TestSearch_SelfQueryRecall(t *testing.T) {
	const n, dim = 2000, 64
	rng := rand.New(rand.NewSource(42))
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = make([]float32, dim)
		for j := range vecs[i] {
			vecs[i][j] = rng.Float32()*2 - 1
		}
	}
	for attempt := 0; attempt < 20; attempt++ {
		g := NewGraph[string]() // default ef=20 is fine for recall@1 here
		for i, v := range vecs {
			g.Add(MakeNode(fmt.Sprintf("d%d", i), v))
		}
		for _, probe := range []int{0, 250, 500, 750, 999, 1500, 1999} {
			nodes := g.Search(vecs[probe], 1)
			want := fmt.Sprintf("d%d", probe)
			if len(nodes) == 0 || nodes[0].Key != want {
				t.Fatalf("attempt %d: self-query for %s got %v", attempt, want, nodes)
			}
		}
	}
}

// TestSearch_TopKOrdering verifies k>1 results come back sorted by
// distance ascending — the contract ann.search relies on.
func TestSearch_TopKOrdering(t *testing.T) {
	g := NewGraph[string]()
	g.Add(
		MakeNode("x", Vector{1, 0, 0, 0}),
		MakeNode("y", Vector{0, 1, 0, 0}),
		MakeNode("z", Vector{0, 0, 1, 0}),
		MakeNode("w", Vector{0.9, 0.1, 0, 0}),
	)
	nodes := g.Search(Vector{1, 0, 0, 0}, 2)
	if len(nodes) != 2 || nodes[0].Key != "x" || nodes[1].Key != "w" {
		t.Errorf("top-2 = %v, want [x w]", nodes)
	}
}
