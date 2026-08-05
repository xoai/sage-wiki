package metrics

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCounterIncAdd(t *testing.T) {
	c := CounterNamed("test_counter_total")
	c.Inc()
	c.Add(4)
	if got := seriesValue(t, "test_counter_total"); got != "5" {
		t.Errorf("counter = %s, want 5", got)
	}
}

func TestNilSafety(t *testing.T) {
	var c *Counter
	var g *Gauge
	var h *Histogram
	c.Inc()
	c.Add(3)
	g.Set(9)
	h.Observe(1.5)
	ObserveDuration(nil, time.Now())
}

func TestGauge(t *testing.T) {
	g := GaugeNamed("test_gauge")
	g.Set(42)
	if got := seriesValue(t, "test_gauge"); got != "42" {
		t.Errorf("gauge = %s, want 42", got)
	}
}

func TestHistogramBuckets(t *testing.T) {
	h := HistogramNamed("test_duration_seconds", []float64{0.1, 0.5, 1})
	h.Observe(0.05) // le=0.1
	h.Observe(0.3)  // le=0.5
	h.Observe(0.7)  // le=1
	h.Observe(9)    // +Inf
	out := exposition(t)
	for _, want := range []string{
		`test_duration_seconds_bucket{le="0.1"} 1`,
		`test_duration_seconds_bucket{le="0.5"} 2`,
		`test_duration_seconds_bucket{le="1"} 3`,
		`test_duration_seconds_bucket{le="+Inf"} 4`,
		`test_duration_seconds_sum 10.05`,
		`test_duration_seconds_count 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n%s", want, out)
		}
	}
}

func TestLazyRegistration(t *testing.T) {
	c := CounterNamed("test_lazy_total")
	_ = c // captured, never recorded
	if strings.Contains(exposition(t), "test_lazy_total") {
		t.Error("zero-activity series appeared in exposition (lazy registration violated)")
	}
	c.Inc()
	if !strings.Contains(exposition(t), "test_lazy_total") {
		t.Error("recorded series missing from exposition")
	}
}

func TestHandleIdentity(t *testing.T) {
	a := CounterNamed("test_identity_total", "pass", "summarize")
	b := CounterNamed("test_identity_total", "pass", "summarize")
	if a != b {
		t.Error("same name+labels yielded different handles")
	}
	c := CounterNamed("test_identity_total", "pass", "write")
	if a == c {
		t.Error("different labels yielded same handle")
	}
}

func TestExpositionFormat(t *testing.T) {
	CounterNamed("test_fmt_total", "stage", "bm25").Inc()
	out := exposition(t)
	for _, want := range []string{
		"# HELP test_fmt_total",
		"# TYPE test_fmt_total counter",
		`test_fmt_total{stage="bm25"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "# HELP") && !strings.HasPrefix(out, "# TYPE") && out != "" {
		// comments/empty are valid
		t.Logf("exposition begins: %.40s", out)
	}
}

func TestExpositionDeterminism(t *testing.T) {
	out1 := exposition(t)
	out2 := exposition(t)
	if out1 != out2 {
		t.Error("exposition not deterministic")
	}
}

func TestSnapshotEmpty(t *testing.T) {
	resetRegistry()
	if got := Snapshot(); got != nil {
		t.Errorf("Snapshot on empty registry = %v, want nil", got)
	}
	CounterNamed("test_snap_total").Add(7)
	got := Snapshot()
	if got == nil {
		t.Fatal("Snapshot nil after recording")
	}
	// alternating key/value with labels in the key
	found := false
	for i := 0; i < len(got); i += 2 {
		if k, ok := got[i].(string); ok && k == "test_snap_total" {
			found = true
			if fmt.Sprint(got[i+1]) != "7" {
				t.Errorf("snapshot value = %v, want 7", got[i+1])
			}
		}
	}
	if !found {
		t.Errorf("snapshot missing series: %v", got)
	}
}

func TestConcurrentRecording(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := CounterNamed("test_race_total")
			for j := 0; j < 100; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()
	if got := seriesValue(t, "test_race_total"); got != "800" {
		t.Errorf("concurrent counter = %s, want 800", got)
	}
}

func TestHandlerOnEmpty(t *testing.T) {
	resetRegistry()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Errorf("empty exposition status = %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("content-type = %q", ct)
	}
}

func BenchmarkHook(b *testing.B) {
	c := CounterNamed("bench_total")
	h := HistogramNamed("bench_seconds", latencyBuckets)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Inc()
		h.Observe(0.001)
	}
}

// SPEC-08 D2: two hardening counters join the inventory with their labels.

func TestSpec08LabelInventory(t *testing.T) {
	limitVals, ok := allowedLabelKV["limit"]
	if !ok {
		t.Fatal("label key \"limit\" missing from allowedLabelKV")
	}
	for _, v := range []string{"doc_bytes", "query_bytes", "compile_batch", "capture_batch", "compile_doc_timeout"} {
		if !limitVals[v] {
			t.Errorf("limit label value %q not permitted", v)
		}
	}
	reasonVals, ok := allowedLabelKV["reason"]
	if !ok {
		t.Fatal("label key \"reason\" missing from allowedLabelKV")
	}
	if !reasonVals["span_missing"] {
		t.Error("reason label value \"span_missing\" not permitted")
	}
}

func TestSpec08HelpTexts(t *testing.T) {
	for _, name := range []string{"limit_exceeded_total", "edge_rejected_total"} {
		if h := helpText(name); h == "auto-generated" || h == "" {
			t.Errorf("helpText(%q) = %q, want a real HELP entry", name, h)
		}
	}
}

func TestSpec08CountersValidateAndExpose(t *testing.T) {
	resetRegistry()
	CounterNamed("limit_exceeded_total", "limit", "doc_bytes").Inc()
	CounterNamed("edge_rejected_total", "reason", "span_missing").Inc()
	if err := ValidateLabels(); err != nil {
		t.Fatalf("ValidateLabels: %v", err)
	}
	out := exposition(t)
	for _, want := range []string{
		`limit_exceeded_total{limit="doc_bytes"} 1`,
		`edge_rejected_total{reason="span_missing"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
}

func TestSpec08LabelViolationCaught(t *testing.T) {
	resetRegistry()
	CounterNamed("limit_exceeded_total", "limit", "not_a_real_limit").Inc()
	if err := ValidateLabels(); err == nil {
		t.Error("ValidateLabels must reject an out-of-inventory limit value")
	}
}
