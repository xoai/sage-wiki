package llm

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func snapshotValue(key string) (any, bool) {
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if k, ok := snap[i].(string); ok && k == key {
			return snap[i+1], true
		}
	}
	return nil, false
}

func TestTrackRecordsTokenMetrics(t *testing.T) {
	metrics.ResetForTest()
	ct := NewCostTracker("anthropic", 0)
	ct.Track("summarize", "claude-test", Usage{InputTokens: 100, OutputTokens: 50, CachedTokens: 25}, false)

	cases := map[string]int64{
		`llm_tokens_total{provider="anthropic",pass="summarize",direction="input"}`:  100,
		`llm_tokens_total{provider="anthropic",pass="summarize",direction="output"}`: 50,
		`llm_tokens_total{provider="anthropic",pass="summarize",direction="cached"}`: 25,
	}
	for key, want := range cases {
		got, ok := snapshotValue(key)
		if !ok {
			t.Errorf("missing series %s", key)
			continue
		}
		if got.(int64) != want {
			t.Errorf("%s = %v, want %d", key, got, want)
		}
	}
}

func TestTrackAccumulatesAcrossCalls(t *testing.T) {
	metrics.ResetForTest()
	ct := NewCostTracker("openai", 0)
	ct.Track("write", "gpt-test", Usage{InputTokens: 10, OutputTokens: 5}, false)
	ct.Track("write", "gpt-test", Usage{InputTokens: 20, OutputTokens: 15}, false)

	got, ok := snapshotValue(`llm_tokens_total{provider="openai",pass="write",direction="input"}`)
	if !ok || got.(int64) != 30 {
		t.Errorf("input tokens = %v %v, want 30", got, ok)
	}
}
