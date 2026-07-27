package query

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

// A config.GraphQueryConfig{} literal yields zeros, and zero is not a usable
// bound for either knob — 0 hops serializes nothing, 0 edges caps everything
// away. Both fall back to defaults.
func TestApplyGraphQueryDefaults(t *testing.T) {
	got := applyGraphQueryDefaults(config.GraphQueryConfig{})

	if got.MaxHops != defaultGraphQueryMaxHops {
		t.Errorf("MaxHops = %d, want %d", got.MaxHops, defaultGraphQueryMaxHops)
	}
	// The LITERAL value, not just the symbol: comparing the constant to
	// itself holds for any value, so it cannot pin the default.
	if got.MaxHops != 2 {
		t.Errorf("default MaxHops = %d, want 2", got.MaxHops)
	}
	if got.MaxEdges != defaultGraphQueryMaxEdges {
		t.Errorf("MaxEdges = %d, want %d", got.MaxEdges, defaultGraphQueryMaxEdges)
	}
	if got.MaxEdges != 60 {
		t.Errorf("default MaxEdges = %d, want 60", got.MaxEdges)
	}
}

// Out-of-range values fall BACK to the default rather than clamping — the
// resolve-threshold rationale: the only safe reading of an out-of-range
// value is "unset". Clamping 999 to 500 would silently honor a typo.
func TestApplyGraphQueryDefaultsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		hops, edges         int
		wantHops, wantEdges int
	}{
		{0, 0, defaultGraphQueryMaxHops, defaultGraphQueryMaxEdges},
		{-1, -5, defaultGraphQueryMaxHops, defaultGraphQueryMaxEdges},
		{6, 501, defaultGraphQueryMaxHops, defaultGraphQueryMaxEdges},
		{1, 1, 1, 1},
		{5, 500, 5, 500},
		{3, 100, 3, 100},
	} {
		got := applyGraphQueryDefaults(config.GraphQueryConfig{MaxHops: tc.hops, MaxEdges: tc.edges})
		if got.MaxHops != tc.wantHops || got.MaxEdges != tc.wantEdges {
			t.Errorf("(%d,%d) = (%d,%d), want (%d,%d)",
				tc.hops, tc.edges, got.MaxHops, got.MaxEdges, tc.wantHops, tc.wantEdges)
		}
	}
}
