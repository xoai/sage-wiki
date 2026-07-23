package hybrid

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	metrics.HistogramNamed("search_duration_seconds", metrics.LatencyBuckets(), "stage", "bm25").Observe(0.01)
	metrics.HistogramNamed("search_duration_seconds", metrics.LatencyBuckets(), "stage", "vector").Observe(0.01)
	metrics.HistogramNamed("search_duration_seconds", metrics.LatencyBuckets(), "stage", "rrf").Observe(0.01)
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
