package community

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
)

// P3-5 T1: Louvain community detection — determinism is the contract.

func trianglePlus() ([]string, []Edge) {
	// Two triangles joined by one bridge: communities {a,b,c} and {d,e,f}.
	nodes := []string{"a", "b", "c", "d", "e", "f"}
	edges := []Edge{
		{"a", "b"}, {"b", "c"}, {"a", "c"},
		{"d", "e"}, {"e", "f"}, {"d", "f"},
		{"c", "d"}, // the bridge
	}
	return nodes, edges
}

func commKey(comms [][]string) string {
	var parts []string
	for _, c := range comms {
		cp := append([]string(nil), c...)
		sort.Strings(cp)
		parts = append(parts, strings.Join(cp, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func TestDetectFindsTwoTriangles(t *testing.T) {
	nodes, edges := trianglePlus()
	levels := Detect(nodes, edges, 4)
	if len(levels) == 0 {
		t.Fatal("no levels returned")
	}
	base := levels[0].Communities
	if len(base) != 2 {
		t.Fatalf("want 2 communities, got %d: %v", len(base), base)
	}
	key := commKey(base)
	if key != "a,b,c|d,e,f" {
		t.Errorf("communities = %s, want a,b,c|d,e,f", key)
	}
}

func TestDetectDeterministic(t *testing.T) {
	nodes, edges := trianglePlus()
	a := Detect(nodes, edges, 4)
	b := Detect(nodes, edges, 4)
	if len(a) != len(b) {
		t.Fatalf("level counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if commKey(a[i].Communities) != commKey(b[i].Communities) {
			t.Fatalf("level %d differs between runs", i)
		}
		// seq assignment must also be identical (IDs are c<level>-<seq>,
		// seq ordered by min member ID)
		for j := range a[i].Communities {
			aj, bj := a[i].Communities[j], b[i].Communities[j]
			if len(aj) != len(bj) {
				t.Fatalf("level %d community %d size differs", i, j)
			}
			for k := range aj {
				if aj[k] != bj[k] {
					t.Fatalf("level %d community %d member %d: %q vs %q", i, j, k, aj[k], bj[k])
				}
			}
		}
	}
}

func TestDetectHierarchyStops(t *testing.T) {
	// A dense clique: one community is the optimal partition, so the
	// hierarchy must stop at a single-community level.
	clique := []string{"a", "b", "c", "d"}
	var edges []Edge
	for i, u := range clique {
		for _, v := range clique[i+1:] {
			edges = append(edges, Edge{u, v})
		}
	}
	levels := Detect(clique, edges, 4)
	if len(levels) != 1 || len(levels[0].Communities) != 1 {
		t.Errorf("clique must yield exactly one single-community level, got %+v", levels)
	}

	// Two triangles: the two-community partition is optimal, so the
	// hierarchy terminates by the level-gain epsilon or the cap — never
	// by merging into one. It must still TERMINATE within the cap.
	nodes, tedges := trianglePlus()
	levels = Detect(nodes, tedges, 4)
	if len(levels) == 0 || len(levels) > 4 {
		t.Fatalf("hierarchy must stop within cap, got %d levels", len(levels))
	}
	if len(levels[len(levels)-1].Communities) != 1 && len(levels) == 4 {
		t.Errorf("top of a capped hierarchy over 2 optimal communities: %+v", levels[len(levels)-1])
	}
}

func TestDetectEmptyAndDegenerate(t *testing.T) {
	if got := Detect(nil, nil, 4); len(got) != 0 {
		t.Errorf("empty input must yield no levels, got %d", len(got))
	}
	// One edge: either one community of two, or two singletons — but never a panic.
	got := Detect([]string{"x", "y"}, []Edge{{"x", "y"}}, 4)
	if len(got) == 0 || len(got[0].Communities) == 0 {
		t.Error("single edge must produce at least one community")
	}
	// Disconnected nodes (no edges): each its own community or none — no panic.
	got = Detect([]string{"x", "y", "z"}, nil, 4)
	total := 0
	for _, l := range got {
		for _, c := range l.Communities {
			total += len(c)
		}
	}
	if total > 3 {
		t.Errorf("membership exceeds node count: %d", total)
	}
}

func TestMemberHashStable(t *testing.T) {
	h1 := MemberHash([]string{"b", "a", "c"})
	h2 := MemberHash([]string{"c", "a", "b"})
	if h1 != h2 {
		t.Error("MemberHash must be order-independent (sorted)")
	}
	want := sha256.Sum256([]byte("a\nb\nc"))
	if h1 != hex.EncodeToString(want[:]) {
		t.Errorf("MemberHash = %q, want sha256 of sorted newline-joined IDs", h1)
	}
	if MemberHash(nil) != MemberHash([]string{}) {
		t.Error("nil and empty must hash identically")
	}
}

// Regression (independent review): at aggregation levels >= 1 the self-loop
// must not inflate the stay side of the gain. Node X carries a self-loop
// (aggregated internal weight) but its cross-community edges favor moving —
// counting the self-loop in k_i,in would over-anchor it.
func TestLocalMovingIgnoresSelfLoopInGain(t *testing.T) {
	// Quotient graph: A-B each with big self-loops (internal weight), X
	// between them but much closer to B. m = 2*(deg sums).
	q := &quotient{
		nodes: []string{"a", "b", "x"},
		adj: map[string]map[string]float64{
			"a": {"a": 10, "x": 1},
			"b": {"b": 10, "x": 5},
			"x": {"a": 1, "b": 5},
		},
		deg: map[string]float64{"a": 11, "b": 15, "x": 6},
		mem: map[string][]string{"a": {"a"}, "b": {"b"}, "x": {"x"}},
		m:   64,
	}
	comms, _ := q.localMoving()
	joined := map[string]string{}
	for _, c := range comms {
		for _, n := range c {
			joined[n] = c[0]
		}
	}
	if joined["x"] != joined["b"] {
		t.Errorf("x must move to b's community (self-loop excluded from gain), got %v", comms)
	}
}
