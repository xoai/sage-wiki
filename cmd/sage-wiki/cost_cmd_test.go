package main

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/llm"
)

func costEvent(pass, provider, model string, tier, input, output int, cost *decimal.Decimal) llm.UsageEvent {
	return llm.UsageEvent{
		TS: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC), Pass: pass,
		Provider: provider, Model: model, Tier: tier,
		InputTokens: input, OutputTokens: output, Cost: cost, PriceSource: "builtin",
	}
}

func dec(v float64) *decimal.Decimal {
	d := decimal.NewFromFloat(v)
	return &d
}

// TestCostReport_Golden is AC-A6: aggregation over a fixture ledger renders
// exact sorted output; unknown-cost rows read "unknown", never "$0.0000".
func TestCostReport_Golden(t *testing.T) {
	events := []llm.UsageEvent{
		costEvent("summarize", "openai", "gpt-4o", 3, 1000, 200, dec(0.0045)),
		costEvent("summarize", "openai", "gpt-4o", 3, 500, 100, dec(0.00225)),
		costEvent("write", "openai", "gpt-4o", 3, 2000, 800, dec(0.013)),
		costEvent("query", "openai-compatible", "deepseek-v4-flash", -1, 100, 20, nil),
	}
	byModel, byPass := aggregateUsage(events, time.Time{})
	got := formatCostReportText(byModel, byPass, time.Time{}, len(events))

	want := `💰 Cost report (from recorded usage)

   By model:
     openai:gpt-4o                                 3 calls       3500 in /      1100 out    ~$0.0198
     openai-compatible:deepseek-v4-flash           1 calls        100 in /        20 out     unknown

   By pass/tier:
     query (tier -)                                1 calls        100 in /        20 out     unknown
     summarize (tier 3)                            2 calls       1500 in /       300 out    ~$0.0068
     write (tier 3)                                1 calls       2000 in /       800 out    ~$0.0130
`
	if got != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(got, "$0.0000") {
		t.Error("unknown cost must NEVER render as $0.0000")
	}
}

func TestCostReport_SinceFilter(t *testing.T) {
	old := costEvent("summarize", "openai", "gpt-4o", 3, 1000, 200, dec(0.0045))
	old.TS = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := costEvent("summarize", "openai", "gpt-4o", 3, 500, 100, dec(0.00225))
	recent.TS = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	since, err := parseSince("2026-07-01")
	if err != nil {
		t.Fatalf("parseSince: %v", err)
	}
	byModel, _ := aggregateUsage([]llm.UsageEvent{old, recent}, since)
	if len(byModel) != 1 || byModel[0].Calls != 1 || byModel[0].Input != 500 {
		t.Errorf("--since filter wrong: %+v", byModel)
	}

	if _, err := parseSince("720h"); err != nil {
		t.Errorf("duration form should parse: %v", err)
	}
	if _, err := parseSince("not-a-date"); err == nil {
		t.Error("garbage --since must error")
	}
}

func TestCostReport_EmptyLedger(t *testing.T) {
	byModel, byPass := aggregateUsage(nil, time.Time{})
	got := formatCostReportText(byModel, byPass, time.Time{}, 0)
	if !strings.Contains(got, "No usage recorded yet") {
		t.Errorf("empty ledger must be a friendly empty report, got:\n%s", got)
	}
}

// TestCostModels_ShowsUserSource is the AC-A3 display half: an overridden
// entry renders with source=user (the precedence half is
// TestLoadRegistry_Precedence in internal/llm).
func TestCostModels_ShowsUserSource(t *testing.T) {
	d := decimal.NewFromFloat(7.7)
	entries := []llm.RegistryEntry{
		{Key: "openai:gpt-4o", Price: llm.Price{InputPerMTok: &d, Source: "user", AsOf: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}},
	}
	got := formatModelsText(entries)
	if !strings.Contains(got, "source=user") {
		t.Errorf("user override must show source=user, got:\n%s", got)
	}
	if !strings.Contains(got, "openai:gpt-4o") || !strings.Contains(got, "in=7.7") {
		t.Errorf("entry row missing key/price:\n%s", got)
	}
}

// TestCostReport_GoldenFromFixture is AC-A6's full pipeline pin: fixture
// usage.jsonl → ReadUsageLog → aggregate → render (also pins the wire
// schema per spec §A.2.4).
func TestCostReport_GoldenFromFixture(t *testing.T) {
	events, err := llm.ReadUsageLog("testdata/usage.jsonl")
	if err != nil {
		t.Fatalf("ReadUsageLog: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 fixture events, got %d", len(events))
	}
	byModel, byPass := aggregateUsage(events, time.Time{})
	got := formatCostReportText(byModel, byPass, time.Time{}, len(events))
	want := `💰 Cost report (from recorded usage)

   By model:
     openai:gpt-4o                                 3 calls       3500 in /      1100 out    ~$0.0198
     openai-compatible:deepseek-v4-flash           1 calls        100 in /        20 out     unknown

   By pass/tier:
     query (tier -)                                1 calls        100 in /        20 out     unknown
     summarize (tier 3)                            2 calls       1500 in /       300 out    ~$0.0068
     write (tier 3)                                1 calls       2000 in /       800 out    ~$0.0130
`
	if got != want {
		t.Errorf("fixture golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestCostReport_MixedPriceSource: a bucket whose events have distinct
// price sources renders "mixed" (F-043) instead of misattributing spend.
func TestCostReport_MixedPriceSource(t *testing.T) {
	e1 := costEvent("summarize", "openai", "gpt-4o", 3, 1000, 200, dec(0.0045))
	e2 := costEvent("summarize", "openai", "gpt-4o", 3, 500, 100, dec(0.00225))
	e2.PriceSource = "user"
	byModel, _ := aggregateUsage([]llm.UsageEvent{e1, e2}, time.Time{})
	if len(byModel) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(byModel))
	}
	if byModel[0].PriceSource != "mixed" {
		t.Errorf("PriceSource = %q, want mixed", byModel[0].PriceSource)
	}
	byPass, _ := aggregateUsage([]llm.UsageEvent{e1}, time.Time{})
	if byPass[0].PriceSource != "builtin" {
		t.Errorf("single-source bucket = %q, want builtin", byPass[0].PriceSource)
	}
}
