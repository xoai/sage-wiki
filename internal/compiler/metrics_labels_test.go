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

// Same contract for resolution (P3-3): without "resolve" in the closed
// inventory the series ships out-of-inventory on /metrics, and ValidateLabels
// only checks series that were actually registered — so nothing fails unless a
// test registers this one.
func TestResolvePassDurationSeriesWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "resolve"), time.Now())
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}

// Same contract for community detection (P3-5): communities.go sets pass
// "communities" on the tracked client; without "communities" in the closed
// pass inventory the series ships out-of-inventory on /metrics. Pin it so
// removing the inventory entry fails CI (SPEC-07 cardinality).
func TestCommunitiesPassDurationSeriesWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "communities"), time.Now())
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
