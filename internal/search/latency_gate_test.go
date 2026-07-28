package search

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// latencyBudget is the V-M5c contract: the unified facade may cost at most
// 15% more per query than the legacy doc-level path it replaces.
const latencyBudget = 1.15

func medianDuration(ds []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// snapshotStageTotal returns the count and total seconds observed on the
// request-scoped stage="total" histogram — the facade's own instrument.
func snapshotStageTotal(t *testing.T) (int64, float64) {
	t.Helper()
	const key = `search_duration_seconds{stage="total"}`
	var count int64
	var sum float64
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		name, _ := snap[i].(string)
		switch name {
		case key + "_count":
			count, _ = snap[i+1].(int64)
		case key + "_sum":
			sum, _ = snap[i+1].(float64)
		}
	}
	return count, sum
}

// V-M5c: the unified pipeline replaces the legacy doc-level path on every
// entry point, so it has to stay within a latency budget of it — a fused
// four-leg search that costs double would be a regression no ranking gain
// pays for. Measurements interleave so machine drift hits both sides.
func TestUnifiedLatencyWithinBudgetOfLegacy(t *testing.T) {
	if testing.Short() {
		t.Skip("latency gate: skipped in -short")
	}

	deps, searcher := benchCorpus(t)
	queries := make([]string, 200)
	for i := range queries {
		queries[i] = fmt.Sprintf("topic%d subject details", i*7%1000)
	}

	runUnified := func(q string) {
		if _, err := Run(context.Background(), deps, Request{Query: q, Limit: 10}); err != nil {
			t.Fatalf("unified: %v", err)
		}
	}
	runLegacy := func(q string) {
		qv, _ := deps.Embedder.Embed(q)
		if _, err := searcher.Search(hybrid.SearchOpts{Query: q, Limit: 10,
			BM25Weight: DefaultBM25Weight, VectorWeight: DefaultVectorWeight}, qv); err != nil {
			t.Fatalf("legacy: %v", err)
		}
	}

	// Warm both paths (SQLite page cache, prepared statements) before timing.
	for i := 0; i < 20; i++ {
		runUnified(queries[i])
		runLegacy(queries[i])
	}

	// Only the measured runs may appear on the stage="total" histogram.
	metrics.ResetForTest()

	const iterations = 101
	unified := make([]time.Duration, 0, iterations)
	legacy := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		q := queries[i%len(queries)]

		start := time.Now()
		runLegacy(q)
		legacy = append(legacy, time.Since(start))

		start = time.Now()
		runUnified(q)
		unified = append(unified, time.Since(start))
	}

	unifiedP50 := medianDuration(unified)
	legacyP50 := medianDuration(legacy)
	ratio := float64(unifiedP50) / float64(legacyP50)
	t.Logf("p50 unified %v, legacy %v (ratio %.2f, budget %.2f)", unifiedP50, legacyP50, ratio, latencyBudget)

	if ratio > latencyBudget {
		t.Errorf("unified p50 %v is %.0f%% of legacy p50 %v, budget is %.0f%%",
			unifiedP50, ratio*100, legacyP50, latencyBudget*100)
	}

	// The facade's own instrument must agree — a stage="total" observation
	// per Run, and a mean within the same budget. Without this the gate
	// would still pass if the metric were silently dropped, and M6's
	// production latency reporting reads exactly this series.
	count, sum := snapshotStageTotal(t)
	if count != iterations {
		t.Fatalf(`search_duration_seconds{stage="total"} count = %d, want %d (one per Run)`, count, iterations)
	}
	instrumentMean := time.Duration(sum / float64(count) * float64(time.Second))
	var legacyTotal time.Duration
	for _, d := range legacy {
		legacyTotal += d
	}
	legacyMean := legacyTotal / time.Duration(len(legacy))
	t.Logf("mean unified (stage=total) %v, legacy %v", instrumentMean, legacyMean)
	if r := float64(instrumentMean) / float64(legacyMean); r > latencyBudget {
		t.Errorf("unified stage=total mean %v is %.0f%% of legacy mean %v, budget is %.0f%%",
			instrumentMean, r*100, legacyMean, latencyBudget*100)
	}
}
