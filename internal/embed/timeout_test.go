package embed

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
)

// SPEC-08 Task 11: the embed HTTP seam carries the workspace
// provider_timeout (the Embedder interface gains no ctx this cycle —
// the client timeout is the per-call bound).

func unwrapForTimeout(e Embedder) Embedder {
	if mw, ok := e.(*metricsWrapper); ok {
		return mw.inner
	}
	return e
}

func embedCallTimeout(e Embedder) (time.Duration, bool) {
	switch v := unwrapForTimeout(e).(type) {
	case *APIEmbedder:
		return v.client.Timeout, true
	case *OllamaEmbedder:
		return v.client.Timeout, true
	default:
		return 0, false
	}
}

func TestNewFromConfigCarriesProviderTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.API.Provider = "openai"
	cfg.API.APIKey = "sk-test"
	cfg.Limits.ProviderTimeout = 7 * time.Second

	e := NewFromConfig(&cfg)
	if e == nil {
		t.Skip("no embedder constructed in this environment")
	}
	got, ok := embedCallTimeout(e)
	if !ok {
		t.Skipf("embedder %T exposes no client timeout", e)
	}
	if got != 7*time.Second {
		t.Errorf("embed call timeout = %v, want 7s", got)
	}
}

func TestNewFromConfigDefaultTimeout(t *testing.T) {
	cfg := config.Defaults()
	cfg.API.Provider = "openai"
	cfg.API.APIKey = "sk-test"

	e := NewFromConfig(&cfg)
	if e == nil {
		t.Skip("no embedder constructed in this environment")
	}
	got, ok := embedCallTimeout(e)
	if !ok {
		t.Skipf("embedder %T exposes no client timeout", e)
	}
	if got != 120*time.Second {
		t.Errorf("embed call timeout = %v, want default 120s", got)
	}
}
