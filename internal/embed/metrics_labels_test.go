package embed

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestHookLabelsWithinInventory(t *testing.T) {
	metrics.ResetForTest()
	wrapMetrics(&fakeEmbedder{}).Embed("x")
	if err := metrics.ValidateLabels(); err != nil {
		t.Fatal(err)
	}
}
