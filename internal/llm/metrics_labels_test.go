package llm

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	// Populate via the Track hook, then validate every registered series.
	metrics.ResetForTest()
	NewCostTracker("anthropic", 0).Track("summarize", "m", Usage{InputTokens: 1, OutputTokens: 1, CachedTokens: 1}, false)
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
