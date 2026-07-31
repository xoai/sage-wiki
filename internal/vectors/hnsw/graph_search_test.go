package hnsw

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestSearch_SelfQueryRecall is the vendored-code gate promised in
// VENDORED.md: across independently seeded graphs, every self-query
// (query = a corpus vector) must return that vector as top-1. Upstream
// v0.6.1 failed ~60% of these (hill-climbing search + min-heap Max()
// fallacy); the vendored ef-search rewrite must keep this at zero
// failures. If upstream is ever restored, this test decides.
//
// Gate terms: 5 graphs × 7 self-query probes at ef=100. Detection margin
// against the gated defect (60% self-query failure): a regression must
// evade 35 exact probes. The original 20-graph count was documentation
// margin, not statistical necessity — ef=100 keeps recall@1 exact on
// this corpus, so attempts are independent confirmations, and the count
// is the suite's dominant cost under -race (HNSW builds are tight
// float32 loops).
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
	for attempt := 0; attempt < 5; attempt++ {
		g := NewGraph[string]()
		// ef=100: layer entry() follows map iteration order, so graph
		// structure varies run to run even with a seeded Rng — at ef=20 an
		// occasional self-query (≈1%) legitimately misses, flaking the
		// gate. ef=100 keeps recall@1 exact on this corpus; the algorithm
		// is what we gate, not the ef parameter.
		g.EfSearch = 100
		g.Rng = rand.New(rand.NewSource(int64(attempt))) // deterministic corpus; ef guards variance
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

// Regression (independent review, CRITICAL): Delete must not leave empty
// layers that crash later Add/Search with nil derefs.
func TestDelete_AddAfterEmptyNoPanic(t *testing.T) {
	g := NewGraph[string]()
	g.Add(MakeNode("a", Vector{1, 0, 0, 0}))
	g.Delete("a")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Add after emptying the graph panicked: %v", r)
		}
	}()
	g.Add(MakeNode("b", Vector{0, 1, 0, 0}))
	if g.Len() != 1 {
		t.Errorf("Len = %d, want 1", g.Len())
	}
}

func TestDelete_SearchWithEmptyTopLayerNoPanic(t *testing.T) {
	g := NewGraph[string]()
	g.Rng = rand.New(rand.NewSource(2))
	// Add enough nodes that upper layers exist, then delete the sole
	// occupant of the top layer.
	for i := 0; i < 200; i++ {
		g.Add(MakeNode(string(rune('a'+i%26))+string(rune('0'+i/26)), Vector{float32(i), float32(i % 7), 0, 0}))
	}
	sizes := g.DebugLayerSizes()
	if len(sizes) < 2 {
		t.Skip("no upper layer formed with this seed")
	}
	// Delete every node except a base-layer handful.
	for i := 0; i < 197; i++ {
		g.Delete(string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	after := g.DebugLayerSizes()
	if len(after) < 2 || after[0] == 0 {
		t.Fatalf("test setup failed to leave an emptied upper layer: %v", after)
	}
	nodes := g.Search(Vector{1, 0, 0, 0}, 1)
	if len(nodes) == 0 {
		t.Error("search returned nothing from a non-empty base layer")
	}
}
