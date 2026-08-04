package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	ct := mustCostTracker(t, "anthropic", 0)
	ct.Track("summarize", "claude-test", Usage{InputTokens: 100, OutputTokens: 50, CachedTokens: 25}, false)

	cases := map[string]int64{
		`llm_tokens_total{provider="anthropic",model="claude-test",pass="summarize",direction="input"}`:  100,
		`llm_tokens_total{provider="anthropic",model="claude-test",pass="summarize",direction="output"}`: 50,
		`llm_tokens_total{provider="anthropic",model="claude-test",pass="summarize",direction="cached"}`: 25,
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
	ct := mustCostTracker(t, "openai", 0)
	ct.Track("write", "gpt-test", Usage{InputTokens: 10, OutputTokens: 5}, false)
	ct.Track("write", "gpt-test", Usage{InputTokens: 20, OutputTokens: 15}, false)

	got, ok := snapshotValue(`llm_tokens_total{provider="openai",model="gpt-test",pass="write",direction="input"}`)
	if !ok || got.(int64) != 30 {
		t.Errorf("input tokens = %v %v, want 30", got, ok)
	}
}

// TestRetryAnd429Counting pins the counting contract (spec §2): each 429
// RESPONSE increments llm_rate_limited_total exactly once; each retry that
// actually runs increments llm_retries_total exactly once.
func TestRetryAnd429Counting(t *testing.T) {
	metrics.ResetForTest()
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count <= 2 {
			w.WriteHeader(429)
			w.Write([]byte("rate limited"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "success after retry"}},
			},
			"model": "gpt-4o",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	client, err := NewClient("openai", "sk-test", server.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatCompletion([]Message{{Role: "user", Content: "test"}}, CallOpts{Model: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}

	if got, ok := snapshotValue("llm_rate_limited_total"); !ok || got.(int64) != 2 {
		t.Errorf("rate_limited_total = %v %v, want 2 (two 429 responses)", got, ok)
	}
	if got, ok := snapshotValue("llm_retries_total"); !ok || got.(int64) != 2 {
		t.Errorf("retries_total = %v %v, want 2 (two retries ran)", got, ok)
	}
}
