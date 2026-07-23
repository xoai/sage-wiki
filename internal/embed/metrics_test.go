package embed

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

type fakeEmbedder struct{ calls int }

func (f *fakeEmbedder) Embed(text string) ([]float32, error) {
	f.calls++
	return []float32{1, 0}, nil
}
func (f *fakeEmbedder) Name() string    { return "fake" }
func (f *fakeEmbedder) Dimensions() int { return 2 }

func TestWrapMetricsCountsCalls(t *testing.T) {
	metrics.ResetForTest()
	fe := &fakeEmbedder{}
	w := wrapMetrics(fe)
	if _, err := w.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Embed("world"); err != nil {
		t.Fatal(err)
	}
	snap := metrics.Snapshot()
	found := false
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == "embed_calls_total" && snap[i+1].(int64) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("embed_calls_total not 2: %v", snap)
	}
	if fe.calls != 2 {
		t.Errorf("underlying calls = %d", fe.calls)
	}
}

func TestWrapMetricsNilPassthrough(t *testing.T) {
	// A nil Embedder must stay interface-nil — every call site guards on
	// embedder != nil (spec: non-nil wrapper holding nil would break them).
	var nilE Embedder
	w := wrapMetrics(nilE)
	if w != nil {
		t.Fatalf("wrapMetrics(nil) = %#v, want nil interface", w)
	}
}

func TestWrapMetricsIdempotent(t *testing.T) {
	fe := &fakeEmbedder{}
	w1 := wrapMetrics(fe)
	w2 := wrapMetrics(w1)
	if w1 != w2 {
		t.Error("double-wrap created a new wrapper (would double-count)")
	}
}
