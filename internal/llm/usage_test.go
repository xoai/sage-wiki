package llm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestUsageEventWireSchema(t *testing.T) {
	cost := decimal.NewFromFloat(1.23)
	ev := UsageEvent{
		TS:               mustTime(t, "2026-07-31T10:00:00Z"),
		Pass:             "summarize",
		Provider:         "openai-compatible",
		Model:            "deepseek-chat",
		Tier:             3,
		InputTokens:      100,
		CachedTokens:     70,
		CacheWriteTokens: 0,
		OutputTokens:     20,
		Cost:             &cost,
		PriceSource:      "builtin",
		Assumptions:      []string{"no batch rate for model; priced at standard rates"},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"ts", "pass", "provider", "model", "tier", "input_tokens", "cached_tokens", "cache_write_tokens", "output_tokens", "cost", "price_source", "assumptions"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("wire schema missing key %q (got %s)", key, raw)
		}
	}
	// Cost marshals as a JSON number, never a string.
	if _, isString := decoded["cost"].(string); isString {
		t.Errorf("cost must be a JSON number, got string in %s", raw)
	}
}

func TestUsageEventNullCost(t *testing.T) {
	ev := UsageEvent{Pass: "query", Tier: -1}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"cost":null`) {
		t.Errorf("unknown cost must serialize as null, got %s", raw)
	}
	if strings.Contains(string(raw), "assumptions") {
		t.Errorf("empty assumptions must be omitted, got %s", raw)
	}
}

func TestFileRecorderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := NewFileRecorder(dir)
	cost := decimal.NewFromFloat(0.0042)
	ev := UsageEvent{
		TS: mustTime(t, "2026-07-31T10:00:00Z"), Pass: "summarize",
		Provider: "openai", Model: "gpt-4o", Tier: 3,
		InputTokens: 1000, OutputTokens: 200, Cost: &cost, PriceSource: "builtin",
	}
	rec.RecordUsage(context.Background(), ev)
	rec.RecordUsage(context.Background(), UsageEvent{TS: mustTime(t, "2026-07-31T10:01:00Z"), Pass: "query", Provider: "openai-compatible", Model: "deepseek-v4-flash", Tier: -1, InputTokens: 50, OutputTokens: 10})

	events, err := ReadUsageLog(filepath.Join(dir, ".sage", "usage.jsonl"))
	if err != nil {
		t.Fatalf("ReadUsageLog: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Cost == nil || !events[0].Cost.Equal(cost) {
		t.Errorf("event 0 cost = %v, want %s", events[0].Cost, cost)
	}
	if events[1].Cost != nil {
		t.Errorf("event 1 (unknown model) cost must be nil, got %v", events[1].Cost)
	}
	if events[1].Tier != -1 {
		t.Errorf("event 1 tier = %d, want -1", events[1].Tier)
	}
}

func TestFileRecorderMissingLog(t *testing.T) {
	events, err := ReadUsageLog(filepath.Join(t.TempDir(), ".sage", "usage.jsonl"))
	if err != nil {
		t.Fatalf("missing log must not error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestClientRecorderFiresOnCompletion(t *testing.T) {
	rec := &captureRecorder{}
	c := &Client{recorder: rec, tier: -1, providerName: "openai-compatible"}
	c.SetPass("expand")
	c.trackUsage(context.Background(), "deepseek-v4-flash", Usage{InputTokens: 10, OutputTokens: 5})
	if len(rec.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Pass != "expand" || ev.Provider != "openai-compatible" || ev.Model != "deepseek-v4-flash" {
		t.Errorf("event identity wrong: %+v", ev)
	}
	if ev.Cost != nil {
		t.Errorf("unknown model must record nil cost, got %v", ev.Cost)
	}
	if ev.Tier != -1 {
		t.Errorf("tier = %d, want -1", ev.Tier)
	}
}

func TestClientRecorderUsesTrackerPricing(t *testing.T) {
	rec := &captureRecorder{}
	ct := mustCostTracker(t, "openai", 0)
	c := &Client{recorder: rec, tracker: ct, tier: 3, providerName: "openai"}
	c.provider = newOpenAIProvider("k", "https://x/v1")
	c.trackUsage(context.Background(), "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if len(rec.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rec.events))
	}
	ev := rec.events[0]
	want := decimal.NewFromFloat(12.5) // 2.5 + 10.0
	if ev.Cost == nil || !ev.Cost.Equal(want) {
		t.Errorf("cost = %v, want %s (tracker/registry price)", ev.Cost, want)
	}
	if ev.PriceSource != "builtin" {
		t.Errorf("PriceSource = %q, want builtin", ev.PriceSource)
	}
	if ev.Tier != 3 {
		t.Errorf("tier = %d, want 3", ev.Tier)
	}
}

type captureRecorder struct{ events []UsageEvent }

func (r *captureRecorder) RecordUsage(_ context.Context, ev UsageEvent) {
	r.events = append(r.events, ev)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
