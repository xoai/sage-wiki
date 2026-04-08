package llm

import (
	"strings"
	"testing"
)

func TestCostTrackerBasic(t *testing.T) {
	ct := NewCostTracker("openai", 0)

	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 200}, false)
	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 800, OutputTokens: 150}, false)
	ct.Track("extract", "gpt-4o", Usage{InputTokens: 500, OutputTokens: 100}, false)

	report := ct.Report()

	if report.TotalInputTokens != 2300 {
		t.Errorf("expected 2300 input tokens, got %d", report.TotalInputTokens)
	}
	if report.TotalOutputTokens != 450 {
		t.Errorf("expected 450 output tokens, got %d", report.TotalOutputTokens)
	}
	if report.EstimatedCost <= 0 {
		t.Error("expected positive cost")
	}
	if len(report.PerPass) != 2 {
		t.Errorf("expected 2 passes, got %d", len(report.PerPass))
	}
	if report.PerPass["summarize"].Calls != 2 {
		t.Errorf("expected 2 summarize calls, got %d", report.PerPass["summarize"].Calls)
	}
}

func TestCostTrackerCacheSavings(t *testing.T) {
	ct := NewCostTracker("openai", 0)

	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 200, CachedTokens: 800}, false)

	report := ct.Report()

	if report.TotalCachedTokens != 800 {
		t.Errorf("expected 800 cached tokens, got %d", report.TotalCachedTokens)
	}
	if report.CacheSavings <= 0 {
		t.Error("expected positive cache savings")
	}
}

func TestCostTrackerUnknownModel(t *testing.T) {
	ct := NewCostTracker("anthropic", 0)

	// Should not panic, should use default pricing
	ct.Track("summarize", "some-unknown-model-2026", Usage{InputTokens: 1000, OutputTokens: 200}, false)

	report := ct.Report()
	if report.EstimatedCost <= 0 {
		t.Error("expected positive cost even for unknown model")
	}
}

func TestCostTrackerOverride(t *testing.T) {
	ct := NewCostTracker("openai", 1.0) // override: $1/1M input tokens

	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 0}, false)

	report := ct.Report()
	// With override: $1/1M input = $1.00
	if report.EstimatedCost < 0.99 || report.EstimatedCost > 1.01 {
		t.Errorf("expected ~$1.00, got $%.4f", report.EstimatedCost)
	}
}

func TestEstimateFromBytes(t *testing.T) {
	tokens, cost := EstimateFromBytes(4000, "gemini", "gemini-2.5-flash", 0)

	if tokens != 1000 {
		t.Errorf("expected 1000 tokens, got %d", tokens)
	}
	if cost <= 0 {
		t.Error("expected positive cost")
	}
}

func TestFormatReport(t *testing.T) {
	report := &CostReport{
		TotalInputTokens:  5000,
		TotalOutputTokens: 1000,
		TotalCachedTokens: 3000,
		TotalTokens:       6000,
		EstimatedCost:     0.0125,
		CacheSavings:      0.0075,
		PerPass: map[string]PassCost{
			"summarize": {Calls: 3, InputTokens: 3000, OutputTokens: 600, Cost: 0.0080},
			"extract":   {Calls: 1, InputTokens: 2000, OutputTokens: 400, Cost: 0.0045},
		},
	}

	output := FormatReport(report)
	if !strings.Contains(output, "Cost report") {
		t.Error("expected 'Cost report' in output")
	}
	if !strings.Contains(output, "cached") {
		t.Error("expected 'cached' in output")
	}
	if !strings.Contains(output, "saved") {
		t.Error("expected 'saved' in output")
	}
}
