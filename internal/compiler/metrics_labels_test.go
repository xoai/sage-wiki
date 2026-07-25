package compiler

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	metrics.HistogramNamed("compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "summarize").Observe(1)
	NewBackpressureController(2).Acquire()()
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}

// The triples pass must emit the same duration series every other pass does,
// or it is invisible in the compile-latency view — and "triples" must be in the
// closed label inventory or the series ships out-of-inventory on /metrics.
func TestTriplesPassDurationSeriesWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "triples"), time.Now())
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
