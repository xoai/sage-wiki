package llm

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	// Populate via the Track hook, then validate every registered series.
	metrics.ResetForTest()
	mustCostTracker(t, "anthropic", 0).Track("summarize", "m", Usage{InputTokens: 1, OutputTokens: 1, CachedTokens: 1}, false)
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}

// The "triples" pass (P3-2) must be in the closed label inventory, or its
// llm_tokens_total series ships out-of-inventory on /metrics with nothing
// failing — ValidateLabels only checks series that were actually registered.
func TestTriplesPassLabelWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	mustCostTracker(t, "anthropic", 0).Track("triples", "m", Usage{InputTokens: 1, OutputTokens: 1}, false)
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}

// SetPass has to be readable for a pass to restore the prior label when it
// finishes; without a getter the cost attribution leaks into whatever runs
// next (on the re-extract path, the whole write pass).
func TestClientPassRoundTrips(t *testing.T) {
	c := &Client{}
	if got := c.Pass(); got != "" {
		t.Errorf("zero-value Pass() = %q, want empty", got)
	}
	c.SetPass("extract")
	prior := c.Pass()
	c.SetPass("triples")
	if got := c.Pass(); got != "triples" {
		t.Errorf("Pass() = %q, want \"triples\"", got)
	}
	c.SetPass(prior)
	if got := c.Pass(); got != "extract" {
		t.Errorf("after restore Pass() = %q, want \"extract\"", got)
	}
}

// The "resolve" pass (P3-3) spends money, so its llm_tokens_total series must
// be in the inventory or the feature is invisible in cost reporting.
func TestResolvePassLabelWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	mustCostTracker(t, "anthropic", 0).Track("resolve", "m", Usage{InputTokens: 1, OutputTokens: 1}, false)
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
