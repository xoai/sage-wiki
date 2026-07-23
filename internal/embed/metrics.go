package embed

import "github.com/xoai/sage-wiki/internal/metrics"

// metricsWrapper counts Embed calls (P2-2). Wrapping is idempotent and
// nil-preserving: a nil Embedder stays interface-nil so every
// `embedder != nil` guard at the call sites is preserved.
type metricsWrapper struct{ inner Embedder }

func (w *metricsWrapper) Embed(text string) ([]float32, error) {
	metrics.CounterNamed("embed_calls_total").Inc()
	return w.inner.Embed(text)
}
func (w *metricsWrapper) Dimensions() int { return w.inner.Dimensions() }
func (w *metricsWrapper) Name() string    { return w.inner.Name() }

// wrapMetrics instruments e. Nil-passthrough and idempotent (spec §2).
func wrapMetrics(e Embedder) Embedder {
	if e == nil {
		return nil
	}
	if _, ok := e.(*metricsWrapper); ok {
		return e
	}
	return &metricsWrapper{inner: e}
}
