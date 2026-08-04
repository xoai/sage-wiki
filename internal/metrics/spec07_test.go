package metrics

import (
	"testing"
)

// TestSpec07LabelInventory: every SPEC-07 label key and value sits inside
// the pinned inventory — record one sample of each new series, then run the
// runtime enumeration. An out-of-inventory label fails here, not in prod.
func TestSpec07LabelInventory(t *testing.T) {
	ResetForTest()

	CounterNamed("compiles_total", "tier", "3", "outcome", "completed").Add(1)
	CounterNamed("compiles_total", "tier", "0", "outcome", "failed").Add(1)
	CounterNamed("compiles_total", "tier", "1", "outcome", "interrupted").Add(1)
	CounterNamed("compiles_total", "tier", "2", "outcome", "cancelled").Add(1)
	HistogramNamed("compile_duration_seconds", CompileBuckets(), "tier", "3").Observe(1.5)
	HistogramNamed("search_channel_duration_seconds", LatencyBuckets(), "channel", "bm25").Observe(0.01)
	HistogramNamed("search_channel_duration_seconds", LatencyBuckets(), "channel", "vector").Observe(0.02)
	HistogramNamed("search_channel_duration_seconds", LatencyBuckets(), "channel", "graph").Observe(0.03)
	GaugeNamed("workspaces_open").Set(2)
	GaugeNamed("job_queue_depth").Set(1)
	CounterNamed("events_dropped_total").Add(3)
	GaugeNamed("mirror_ship_lag_seconds").Set(30)
	// model is key-only (provider-defined, unbounded) — arbitrary values pass.
	CounterNamed("llm_tokens_total", "provider", "openai", "model", "gpt-4o-mini-2024", "pass", "summarize", "direction", "input").Add(10)

	if err := ValidateLabels(); err != nil {
		t.Fatalf("SPEC-07 series outside the pinned inventory: %v", err)
	}
}

// TestSpec07LabelInventoryRejects: a value outside the pinned set is
// reported — the enumeration is a guard, not a formality.
func TestSpec07LabelInventoryRejects(t *testing.T) {
	ResetForTest()
	CounterNamed("compiles_total", "tier", "9", "outcome", "completed").Add(1)
	if err := ValidateLabels(); err == nil {
		t.Fatal("tier=9 must be rejected by the inventory")
	}
	ResetForTest()
	CounterNamed("compiles_total", "tier", "3", "outcome", "exploded").Add(1)
	if err := ValidateLabels(); err == nil {
		t.Fatal("outcome=exploded must be rejected by the inventory")
	}
}

// TestGaugeIncDecAdd: the SPEC-07 gauge arithmetic (open workspaces,
// queue depth) is exact.
func TestGaugeIncDecAdd(t *testing.T) {
	ResetForTest()
	g := GaugeNamed("workspaces_open")
	g.Inc()
	g.Inc()
	g.Dec()
	if got := gaugeValue(t, "workspaces_open"); got != 1 {
		t.Errorf("gauge = %d, want 1", got)
	}
	g.Add(5)
	if got := gaugeValue(t, "workspaces_open"); got != 6 {
		t.Errorf("gauge = %d, want 6", got)
	}
	var nilGauge *Gauge
	nilGauge.Inc() // nil-safe, must not panic
	nilGauge.Dec()
	nilGauge.Add(1)
}

func gaugeValue(t *testing.T, name string) int64 {
	t.Helper()
	snap := Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == name {
			return snap[i+1].(int64)
		}
	}
	t.Fatalf("series %s not registered", name)
	return 0
}
