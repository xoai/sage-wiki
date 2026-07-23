package compiler

import (
	"testing"

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
