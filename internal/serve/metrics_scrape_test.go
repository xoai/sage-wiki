package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/xoai/sage-wiki/internal/metrics"
)

// TestMetricsScrapeSpec07 (AC-4): /metrics exposes every SPEC-07 series
// with the right type, parsed by the Prometheus client parser (expfmt).
// Series register LAZILY at first record (metrics design D8), so the test
// records one sample of each series before scraping — a scrape-only test
// would see a partial registry and pass vacuously.
func TestMetricsScrapeSpec07(t *testing.T) {
	metrics.ResetForTest()

	// One sample per SPEC-07 series (the spec §5 minimum set).
	metrics.CounterNamed("compiles_total", "tier", "3", "outcome", "completed").Add(1)
	metrics.HistogramNamed("compile_duration_seconds", metrics.CompileBuckets(), "tier", "3").Observe(0.5)
	metrics.CounterNamed("llm_tokens_total", "provider", "openai", "model", "gpt-4o-mini", "pass", "summarize", "direction", "input").Add(100)
	metrics.CounterNamed("llm_tokens_total", "provider", "openai", "model", "gpt-4o-mini", "pass", "summarize", "direction", "cached").Add(25)
	metrics.HistogramNamed("search_duration_seconds", metrics.LatencyBuckets(), "stage", "total").Observe(0.1)
	metrics.HistogramNamed("search_channel_duration_seconds", metrics.LatencyBuckets(), "channel", "bm25").Observe(0.02)
	metrics.GaugeNamed("workspaces_open").Set(1)
	metrics.GaugeNamed("job_queue_depth").Set(2)
	metrics.CounterNamed("events_dropped_total").Add(3)
	metrics.GaugeNamed("mirror_ship_lag_seconds").Set(12)

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}

	var parser = expfmt.NewTextParser(model.LegacyValidation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatalf("expfmt parse: %v", err)
	}

	want := map[string]dto.MetricType{
		"compiles_total":                  dto.MetricType_COUNTER,
		"compile_duration_seconds":        dto.MetricType_HISTOGRAM,
		"llm_tokens_total":                dto.MetricType_COUNTER,
		"search_duration_seconds":         dto.MetricType_HISTOGRAM,
		"search_channel_duration_seconds": dto.MetricType_HISTOGRAM,
		"workspaces_open":                 dto.MetricType_GAUGE,
		"job_queue_depth":                 dto.MetricType_GAUGE,
		"events_dropped_total":            dto.MetricType_COUNTER,
		"mirror_ship_lag_seconds":         dto.MetricType_GAUGE,
	}
	for name, typ := range want {
		fam, ok := fams[name]
		if !ok {
			t.Errorf("series %s missing from /metrics", name)
			continue
		}
		if fam.GetType() != typ {
			t.Errorf("%s type = %s, want %s", name, fam.GetType(), typ)
		}
		if len(fam.GetMetric()) == 0 {
			t.Errorf("%s has no samples", name)
		}
	}
}
