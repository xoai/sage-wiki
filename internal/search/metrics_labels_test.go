package search

import (
	"context"
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

// The facade's request-scoped stage="total" observation must be inside the
// pinned label inventory (spec §7.2). It was not when it shipped: the
// inventory listed only the per-leg stages, and no test in this package
// called ValidateLabels, so an out-of-inventory label passed CI.
func TestSearchMetricsLabelsAreInInventory(t *testing.T) {
	metrics.ResetForTest()

	deps, _ := benchCorpus(t)
	if _, err := Run(context.Background(), deps, Request{Query: "topic1 subject details", Limit: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := metrics.ValidateLabels(); err != nil {
		t.Errorf("search emitted labels outside the pinned inventory: %v", err)
	}
}
