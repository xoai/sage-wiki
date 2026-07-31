package llm

import (
	"os"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := loadRegistryFrom("", t.TempDir()+"/absent.json")
	if err != nil {
		t.Fatalf("loadRegistryFrom: %v", err)
	}
	return r
}

func TestCostTrackerBasic(t *testing.T) {
	ct := newCostTrackerWithRegistry("openai", 0, testRegistry(t))

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
	if report.Cost == nil || !report.Cost.GreaterThan(decimal.Zero) {
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
	ct := newCostTrackerWithRegistry("openai", 0, testRegistry(t))

	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 200, CachedTokens: 800}, false)

	report := ct.Report()

	if report.TotalCachedTokens != 800 {
		t.Errorf("expected 800 cached tokens, got %d", report.TotalCachedTokens)
	}
	if report.CacheSavings == nil || !report.CacheSavings.GreaterThan(decimal.Zero) {
		t.Error("expected positive cache savings")
	}
}

// TestCost_OpenAICompatible_UnknownModelNeverPricedAsOpenAI is the SPEC-05
// regression test named for the bug (AC-A1): an openai-compatible endpoint
// with a registry entry is priced from the registry; without one the cost
// is nil — and NEITHER case ever returns an OpenAI price or the old
// 0.50/2.0 default.
func TestCost_OpenAICompatible_UnknownModelNeverPricedAsOpenAI(t *testing.T) {
	// Case 1: registry entry present (builtin openai-compatible:deepseek-chat).
	ct := newCostTrackerWithRegistry("openai-compatible", 0, testRegistry(t))
	ct.Track("summarize", "deepseek-chat", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, false)
	report := ct.Report()
	if report.Cost == nil {
		t.Fatal("deepseek-chat has a builtin registry entry — cost must be known")
	}
	// deepseek-chat: $0.27/1M input + $1.10/1M output = $1.37 total.
	want := decimal.NewFromFloat(0.27).Add(decimal.NewFromFloat(1.10))
	if !report.Cost.Equal(want) {
		t.Errorf("Cost = %s, want %s (registry deepseek-chat price)", report.Cost, want)
	}
	// Guard against the defect: OpenAI's gpt-4o price for the same usage
	// would be 2.5 + 10.0 = 12.5; the old flat default would be 0.5 + 2.0 = 2.5.
	if report.Cost.Equal(decimal.NewFromFloat(12.5)) {
		t.Error("cost matches OpenAI gpt-4o pricing — the SPEC-05 defect is back")
	}
	if report.Cost.Equal(decimal.NewFromFloat(2.5)) {
		t.Error("cost matches the old flat 0.50/2.0 default — fabricated price")
	}

	// Case 2: no registry entry → Cost nil, never a price.
	ct2 := newCostTrackerWithRegistry("openai-compatible", 0, testRegistry(t))
	ct2.Track("summarize", "deepseek-v4-flash", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, false)
	report2 := ct2.Report()
	if report2.Cost != nil {
		t.Errorf("unknown model must yield nil cost, got %s", report2.Cost)
	}
	if len(report2.UnknownModels) != 1 || report2.UnknownModels[0] != "openai-compatible:deepseek-v4-flash" {
		t.Errorf("UnknownModels = %v, want [openai-compatible:deepseek-v4-flash]", report2.UnknownModels)
	}
	if report2.TotalInputTokens != 1_000_000 {
		t.Error("token totals must stay exact even when cost is unknown")
	}

	// Case 3: ollama (never had even the aliasing shim) → nil as well.
	ct3 := newCostTrackerWithRegistry("ollama", 0, testRegistry(t))
	ct3.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 100}, false)
	if report3 := ct3.Report(); report3.Cost != nil {
		t.Errorf("ollama:gpt-4o must never resolve to OpenAI's price, got %s", report3.Cost)
	}
}

// TestCost_BlendedCacheSplit is AC-A2: a 70%-cached fixture at DeepSeek-like
// prices computes the blended cost correctly to the cent.
func TestCost_BlendedCacheSplit(t *testing.T) {
	ct := newCostTrackerWithRegistry("openai-compatible", 0, testRegistry(t))
	// 1M total input, 70% cached, 100k output — deepseek-chat prices
	// ($0.27 input, $0.07 cached, $1.10 output per 1M).
	ct.Track("summarize", "deepseek-chat", Usage{InputTokens: 1_000_000, CachedTokens: 700_000, OutputTokens: 100_000}, false)
	report := ct.Report()
	if report.Cost == nil {
		t.Fatal("cost must be known for deepseek-chat")
	}
	// 300k * 0.27/1M + 700k * 0.07/1M + 100k * 1.10/1M = 0.081 + 0.049 + 0.11 = 0.24
	want := decimal.NewFromFloat(0.24)
	if !report.Cost.Equal(want) {
		t.Errorf("blended cost = %s, want %s (exact to the cent)", report.Cost.StringFixed(4), want.StringFixed(4))
	}
}

func TestCost_CacheWritePricedAtCacheWriteRate(t *testing.T) {
	ct := newCostTrackerWithRegistry("anthropic", 0, testRegistry(t))
	// claude-sonnet-4: input 3.0, cached 0.3, cache_write 3.75, output 15.0.
	// 1M input with 500k cache-read + 200k cache-write + 100k output:
	// 500k*3.0 + 500k*0.3 + 200k*3.75 + 100k*15.0 (per 1M) = 1.5+0.15+0.75+1.5 = 3.90
	ct.Track("summarize", "claude-sonnet-4-20250514", Usage{InputTokens: 1_000_000, CachedTokens: 500_000, CacheWriteTokens: 200_000, OutputTokens: 100_000}, false)
	report := ct.Report()
	want := decimal.NewFromFloat(3.90)
	if report.Cost == nil || !report.Cost.Equal(want) {
		t.Errorf("Cost = %v, want %s", report.Cost, want)
	}
}

func TestCost_BatchWithoutBatchRateAssumption(t *testing.T) {
	ct := newCostTrackerWithRegistry("openai-compatible", 0, testRegistry(t))
	// deepseek-chat has no batch rates → standard rates + assumption.
	ct.Track("summarize", "deepseek-chat", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, true)
	report := ct.Report()
	want := decimal.NewFromFloat(0.27).Add(decimal.NewFromFloat(1.10))
	if report.Cost == nil || !report.Cost.Equal(want) {
		t.Errorf("Cost = %v, want %s (standard rates)", report.Cost, want)
	}
	batchNote := false
	for _, a := range report.Assumptions {
		if strings.Contains(a, "no batch rate") {
			batchNote = true
		}
	}
	if !batchNote {
		t.Errorf("Assumptions = %v, want no-batch-rate note", report.Assumptions)
	}
}

func TestCostTrackerOverride(t *testing.T) {
	ct := newCostTrackerWithRegistry("openai", 1.0, testRegistry(t)) // override: $1/1M input tokens

	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1_000_000, OutputTokens: 0}, false)

	report := ct.Report()
	if report.Cost == nil {
		t.Fatal("override must price any model")
	}
	if diff := report.Cost.Sub(decimal.NewFromInt(1)).Abs(); diff.GreaterThan(decimal.NewFromFloat(0.0001)) {
		t.Errorf("expected ~$1.00, got $%s", report.Cost.StringFixed(4))
	}
}

func TestEstimateFromBytes(t *testing.T) {
	tokens, cost, err := estimateFromBytesWithRegistry(4000, "gemini", "gemini-2.5-flash", 0, testRegistry(t))
	if err != nil {
		t.Fatalf("estimateFromBytesWithRegistry: %v", err)
	}
	if tokens != 1000 {
		t.Errorf("expected 1000 tokens, got %d", tokens)
	}
	if cost == nil || !cost.GreaterThan(decimal.Zero) {
		t.Error("expected positive cost")
	}
}

func TestEstimateFromBytes_UnknownModel(t *testing.T) {
	_, cost, err := estimateFromBytesWithRegistry(4000, "openai-compatible", "deepseek-v4-flash", 0, testRegistry(t))
	if err != nil {
		t.Fatalf("estimateFromBytesWithRegistry: %v", err)
	}
	if cost != nil {
		t.Errorf("unknown model estimate must be nil, got %s", cost)
	}
}

func TestFormatReport(t *testing.T) {
	cost := decimal.NewFromFloat(0.0125)
	savings := decimal.NewFromFloat(0.0075)
	passCost := decimal.NewFromFloat(0.0080)
	passCost2 := decimal.NewFromFloat(0.0045)
	report := &CostReport{
		TotalInputTokens:  5000,
		TotalOutputTokens: 1000,
		TotalCachedTokens: 3000,
		TotalTokens:       6000,
		Cost:              &cost,
		CacheSavings:      &savings,
		PerPass: map[string]PassCost{
			"summarize": {Calls: 3, InputTokens: 3000, OutputTokens: 600, Cost: &passCost},
			"extract":   {Calls: 1, InputTokens: 2000, OutputTokens: 400, Cost: &passCost2},
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

func TestFormatReport_UnknownCost(t *testing.T) {
	report := &CostReport{
		TotalInputTokens:  5000,
		TotalOutputTokens: 1000,
		UnknownModels:     []string{"openai-compatible:deepseek-v4-flash"},
		PerPass:           map[string]PassCost{"summarize": {Calls: 1}},
	}
	output := FormatReport(report)
	if !strings.Contains(output, "unknown (model not in price registry") {
		t.Errorf("unknown cost must render as unknown, got:\n%s", output)
	}
	if strings.Contains(output, "$0.0000") {
		t.Error("unknown cost must NEVER render as $0.0000")
	}
}

// TestLoadPriceTable_PartialLegacyEntryIsUnknown documents the deliberate
// behavior change for partial legacy PERF-04 entries: an entry that sets
// only some fields yields nil for the rest (old behavior: zero fields
// silently priced those components FREE — a fabricated number). The model
// now reports unknown cost rather than a partial price.
func TestLoadPriceTable_PartialLegacyEntryIsUnknown(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 9.99}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 100}, false)
	report := ct.Report()
	if report.Cost != nil {
		t.Errorf("partial legacy entry (no output price) must yield unknown cost, got %s", report.Cost)
	}
}

// mustCostTracker builds a tracker over the builtin registry only — hermetic,
// never reads the real ~/.sage-wiki/prices.json.
func mustCostTracker(t *testing.T, provider string, override float64) *CostTracker {
	t.Helper()
	return newCostTrackerWithRegistry(provider, override, testRegistry(t))
}

// TestCost_NoCacheSplitReportedAssumption pins the spec §A.2.2 assumption:
// when a provider response carries no cache split, the report says so.
func TestCost_NoCacheSplitReportedAssumption(t *testing.T) {
	ct := mustCostTracker(t, "openai", 0)
	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 100}, false)
	report := ct.Report()
	found := false
	for _, a := range report.Assumptions {
		if a == "no cache split reported" {
			found = true
		}
	}
	if !found {
		t.Errorf("Assumptions = %v, want 'no cache split reported'", report.Assumptions)
	}
}

// TestCost_UserOverridePrecedence is AC-A3's verbatim test: builtin <
// user file < workspace table < token_price_per_million.
func TestCost_UserOverridePrecedence(t *testing.T) {
	dir := t.TempDir()
	userPath := dir + "/user.json"
	wsPath := dir + "/ws.json"
	if err := os.WriteFile(userPath, []byte(`{"prices": {"openai:gpt-4o": {"input": "9.9", "output": "9.9", "as_of": "2026-01-01"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte(`{"openai": {"gpt-4o": {"input": 7.7, "output": 7.7}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Workspace beats user beats builtin.
	r, err := loadRegistryFrom(wsPath, userPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := newCostTrackerWithRegistry("openai", 0, r).priceFor("gpt-4o")
	if p.InputPerMTok.String() != "7.7" || p.Source != PriceSourceUser {
		t.Errorf("workspace should win: %+v", p)
	}
	// User beats builtin when workspace silent.
	r2, _ := loadRegistryFrom("", userPath)
	p2, _ := newCostTrackerWithRegistry("openai", 0, r2).priceFor("gpt-4o")
	if p2.InputPerMTok.String() != "9.9" {
		t.Errorf("user should beat builtin: %+v", p2)
	}
	// token_price_per_million beats everything.
	p3, _ := newCostTrackerWithRegistry("openai", 3.0, r).priceFor("gpt-4o")
	if p3.InputPerMTok.String() != "3" {
		t.Errorf("explicit override should win: %+v", p3)
	}
}

func TestEstimateFromBytes_Hermetic(t *testing.T) {
	r := testRegistry(t)
	_, cost, err := estimateFromBytesWithRegistry(4000, "gemini", "gemini-2.5-flash", 0, r)
	if err != nil {
		t.Fatal(err)
	}
	if cost == nil || !cost.GreaterThan(decimal.Zero) {
		t.Error("expected positive cost")
	}
	_, unknown, err := estimateFromBytesWithRegistry(4000, "openai-compatible", "deepseek-v4-flash", 0, r)
	if err != nil {
		t.Fatal(err)
	}
	if unknown != nil {
		t.Errorf("unknown model estimate must be nil, got %s", unknown)
	}
}

func TestEstimateVariants_RegistryRates(t *testing.T) {
	r := testRegistry(t)
	ct := newCostTrackerWithRegistry("openai", 0, r)
	price, ok := ct.priceFor("gpt-4o")
	if !ok {
		t.Fatal("gpt-4o priced")
	}
	if price.BatchInputPerMTok == nil || price.CachedInputPerMTok == nil {
		t.Fatal("gpt-4o must carry batch + cached rates for this test")
	}
	// Variants computed from registry rates, not multipliers: batch uses
	// 1.25/5.0 (half of 2.5/10.0 for gpt-4o), cached uses 1.25 input.
	// 1M bytes → 250k input tokens, 62.5k output.
	// standard = 250000*2.5/1M + 62500*10/1M = 0.625 + 0.625 = 1.25
	// batch    = 250000*1.25/1M + 62500*5/1M = 0.3125 + 0.3125 = 0.625
	// cached   = 250000*1.25/1M + 62500*10/1M = 0.3125 + 0.625 = 0.9375
	est := estimateVariantsWithRegistry(1_000_000, "openai", "gpt-4o", 0, testRegistry(t))
	if est.Standard == nil || !est.Standard.Equal(decimal.NewFromFloat(1.25)) {
		t.Errorf("Standard = %v, want 1.25", est.Standard)
	}
	if est.Batch == nil || !est.Batch.Equal(decimal.NewFromFloat(0.625)) {
		t.Errorf("Batch = %v, want 0.625", est.Batch)
	}
	if est.Cached == nil || !est.Cached.Equal(decimal.NewFromFloat(0.9375)) {
		t.Errorf("Cached = %v, want 0.9375", est.Cached)
	}
}

// TestEstimateVariants_NoRatesOmitted: a model without batch/cached rates
// yields nil variants (the CLI omits those lines — no invented multipliers).
func TestEstimateVariants_NoRatesOmitted(t *testing.T) {
	est := estimateVariantsWithRegistry(1_000_000, "openai-compatible", "deepseek-chat", 0, testRegistry(t))
	if est.Standard == nil {
		t.Fatal("deepseek-chat standard estimate must exist")
	}
	if est.Batch != nil {
		t.Errorf("no batch rates in registry → Batch must be nil, got %v", est.Batch)
	}
	if est.Cached == nil {
		t.Error("deepseek-chat HAS a cached rate — Cached must be set")
	}
}
