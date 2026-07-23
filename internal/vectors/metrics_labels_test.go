package vectors

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	// The cache test in this package already populated hits/misses.
	metrics.CounterNamed("vector_cache_hits_total", "cache", "doc").Inc()
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
