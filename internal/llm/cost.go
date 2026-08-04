package llm

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/metrics"
)

// ModelPrice is the legacy PERF-04 workspace price-table file shape. New
// code uses Price (pricing.go); this type exists only so legacy table files
// keep loading.
type ModelPrice struct {
	Input       float64 `json:"input,omitempty"`        // $ per 1M input tokens
	Output      float64 `json:"output,omitempty"`       // $ per 1M output tokens
	CachedInput float64 `json:"cached_input,omitempty"` // $ per 1M cached input tokens
	BatchInput  float64 `json:"batch_input,omitempty"`  // $ per 1M batch input tokens (0 = not supported)
	BatchOutput float64 `json:"batch_output,omitempty"` // $ per 1M batch output tokens
}

// CostEntry records token usage for a single LLM call.
type CostEntry struct {
	Pass             string // summarize, extract, write, query, lint
	Model            string
	Provider         string
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	CacheWriteTokens int
	BatchMode        bool
}

// CostReport summarizes total cost for a compile. Cost is nil when any
// model's price is unknown — an unknown cost is never rendered as zero
// (AGENTS.md rule 5); UnknownModels names the culprits and the token
// totals remain exact.
type CostReport struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCachedTokens int
	TotalTokens       int
	Cost              *decimal.Decimal
	CacheSavings      *decimal.Decimal
	PerPass           map[string]PassCost
	Assumptions       []string // sorted, deduped
	UnknownModels     []string // sorted, deduped — provider:model with nil cost
}

// PassCost holds cost info for a single compiler pass. Cost is nil when
// any call in the pass used a model whose price is unknown.
type PassCost struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
	Cost         *decimal.Decimal
	Calls        int
}

// CostTracker accumulates token usage across a compile session and prices
// it against a Registry. It never falls back to another provider's table
// and never invents a default price.
type CostTracker struct {
	mu       sync.Mutex
	entries  []CostEntry
	provider string
	override float64 // user config override price per 1M input tokens
	registry *Registry
}

// NewCostTracker creates a tracker for the given provider using the
// builtin + user-file registry.
func NewCostTracker(provider string, priceOverride float64) (*CostTracker, error) {
	return NewCostTrackerWithTable(provider, priceOverride, "")
}

// NewCostTrackerWithTable creates a tracker whose registry also overlays
// the workspace price table (legacy PERF-04 or registry shape). A malformed
// registry file is a hard error.
func NewCostTrackerWithTable(provider string, priceOverride float64, tablePath string) (*CostTracker, error) {
	r, err := LoadRegistry(tablePath)
	if err != nil {
		return nil, err
	}
	return &CostTracker{provider: provider, override: priceOverride, registry: r}, nil
}

// newCostTrackerWithRegistry builds a tracker from an explicit registry —
// the test seam that keeps registry-loading failures out of unit tests.
func newCostTrackerWithRegistry(provider string, priceOverride float64, r *Registry) *CostTracker {
	return &CostTracker{provider: provider, override: priceOverride, registry: r}
}

// Track records a single LLM call's usage.
// Also THE single token-metrics hook (P2-2): fires for sync (client.go) and
// batch (pipeline.go) paths — no second recording site exists.
// SPEC-07: the model label extends the series for per-model token
// accounting (cached/uncached split stays on the direction label).
func (ct *CostTracker) Track(pass string, model string, usage Usage, batch bool) {
	metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "model", model, "pass", pass, "direction", "input").Add(int64(usage.InputTokens))
	metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "model", model, "pass", pass, "direction", "output").Add(int64(usage.OutputTokens))
	if usage.CachedTokens > 0 {
		metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "model", model, "pass", pass, "direction", "cached").Add(int64(usage.CachedTokens))
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.entries = append(ct.entries, CostEntry{
		Pass:             pass,
		Model:            model,
		Provider:         ct.provider,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CachedTokens:     usage.CachedTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		BatchMode:        batch,
	})
}

// priceFor resolves the price for a model: explicit override wins, then the
// registry for THIS provider only. Unknown is (nil, false) — never a guess.
func (ct *CostTracker) priceFor(model string) (*Price, bool) {
	if ct.override > 0 {
		return &Price{
			InputPerMTok:       decimalPtr(decimal.NewFromFloat(ct.override)),
			CachedInputPerMTok: decimalPtr(decimal.NewFromFloat(ct.override * 0.1)),
			OutputPerMTok:      decimalPtr(decimal.NewFromFloat(ct.override * 3)),
			Source:             PriceSourceUser,
		}, true
	}
	return ct.registry.Lookup(ct.provider, model)
}

func decimalPtr(d decimal.Decimal) *decimal.Decimal { return &d }

var million = decimal.NewFromInt(1_000_000)

// calculateCost prices one entry. Returns (nil, nil) when the model is
// unknown; otherwise the cost and any assumptions applied.
func (ct *CostTracker) calculateCost(e CostEntry) (*decimal.Decimal, []string) {
	price, ok := ct.priceFor(e.Model)
	if !ok {
		return nil, nil
	}
	var assumptions []string

	uncached := e.InputTokens - e.CachedTokens
	if uncached < 0 {
		uncached = 0
	}

	if price.InputPerMTok == nil || price.OutputPerMTok == nil {
		return nil, nil // a nil component makes the whole cost unknown
	}

	inputRate := price.InputPerMTok
	outputRate := price.OutputPerMTok
	if e.BatchMode {
		if price.BatchInputPerMTok != nil && price.BatchOutputPerMTok != nil {
			inputRate = price.BatchInputPerMTok
			outputRate = price.BatchOutputPerMTok
		} else {
			assumptions = append(assumptions, "no batch rate for model; priced at standard rates")
		}
	}

	cost := inputRate.Mul(decimal.NewFromInt(int64(uncached))).Div(million)
	cost = cost.Add(outputRate.Mul(decimal.NewFromInt(int64(e.OutputTokens))).Div(million))

	if e.CachedTokens == 0 && e.CacheWriteTokens == 0 {
		assumptions = append(assumptions, "no cache split reported")
	}
	if e.CachedTokens > 0 {
		if price.CachedInputPerMTok != nil {
			cost = cost.Add(price.CachedInputPerMTok.Mul(decimal.NewFromInt(int64(e.CachedTokens))).Div(million))
		} else {
			cost = cost.Add(price.InputPerMTok.Mul(decimal.NewFromInt(int64(e.CachedTokens))).Div(million))
			assumptions = append(assumptions, "cached tokens priced at standard input rate")
		}
	}
	if e.CacheWriteTokens > 0 {
		if price.CacheWritePerMTok != nil {
			cost = cost.Add(price.CacheWritePerMTok.Mul(decimal.NewFromInt(int64(e.CacheWriteTokens))).Div(million))
		} else {
			cost = cost.Add(price.InputPerMTok.Mul(decimal.NewFromInt(int64(e.CacheWriteTokens))).Div(million))
			assumptions = append(assumptions, "cache-write tokens priced at standard input rate")
		}
	}

	return &cost, assumptions
}

// calculateSavings returns what caching saved on this entry, or nil when
// the saving can't be known (unknown model, or cached tokens billed at the
// standard rate — no discount to count).
func (ct *CostTracker) calculateSavings(e CostEntry) *decimal.Decimal {
	if e.CachedTokens == 0 {
		z := decimal.Zero
		return &z
	}
	price, ok := ct.priceFor(e.Model)
	if !ok || price.InputPerMTok == nil || price.CachedInputPerMTok == nil {
		return nil
	}
	full := price.InputPerMTok.Mul(decimal.NewFromInt(int64(e.CachedTokens))).Div(million)
	discounted := price.CachedInputPerMTok.Mul(decimal.NewFromInt(int64(e.CachedTokens))).Div(million)
	savings := full.Sub(discounted)
	return &savings
}

// PriceUsage prices a single call without recording it: cost (nil when
// unknown), the price source, and any assumptions applied. Used by the
// usage-event ledger; Report uses the same math via calculateCost.
func (ct *CostTracker) PriceUsage(model string, usage Usage, batch bool) (*decimal.Decimal, string, []string) {
	entry := CostEntry{
		Model:            model,
		Provider:         ct.provider,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CachedTokens:     usage.CachedTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		BatchMode:        batch,
	}
	cost, assumptions := ct.calculateCost(entry)
	source := ""
	if p, ok := ct.priceFor(model); ok {
		source = p.Source
	}
	return cost, source, assumptions
}

// Report generates the cost summary. Any unknown model poisons the dollar
// totals to nil (unknown is never a fabricated partial sum); token totals
// stay exact.
func (ct *CostTracker) Report() *CostReport {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	report := &CostReport{PerPass: make(map[string]PassCost)}
	total := decimal.Zero
	savings := decimal.Zero
	unknownCost := false
	unknownSavings := false
	assumptionSet := map[string]bool{}
	unknownSet := map[string]bool{}
	passUnknown := map[string]bool{}

	for _, e := range ct.entries {
		report.TotalInputTokens += e.InputTokens
		report.TotalOutputTokens += e.OutputTokens
		report.TotalCachedTokens += e.CachedTokens
		report.TotalTokens += e.InputTokens + e.OutputTokens + e.CacheWriteTokens

		cost, assumptions := ct.calculateCost(e)
		for _, a := range assumptions {
			assumptionSet[a] = true
		}

		if cost == nil {
			unknownCost = true
			unknownSet[e.Provider+":"+e.Model] = true
			passUnknown[e.Pass] = true
		} else {
			total = total.Add(*cost)
		}

		if s := ct.calculateSavings(e); s == nil {
			unknownSavings = true
		} else {
			savings = savings.Add(*s)
		}

		pc := report.PerPass[e.Pass]
		pc.InputTokens += e.InputTokens
		pc.OutputTokens += e.OutputTokens
		pc.CachedTokens += e.CachedTokens
		if cost != nil {
			if pc.Cost == nil {
				pc.Cost = &decimal.Decimal{}
			}
			*pc.Cost = pc.Cost.Add(*cost)
		}
		pc.Calls++
		report.PerPass[e.Pass] = pc
	}

	for pass := range passUnknown {
		pc := report.PerPass[pass]
		pc.Cost = nil
		report.PerPass[pass] = pc
	}
	if !unknownCost {
		report.Cost = &total
	}
	if !unknownSavings {
		report.CacheSavings = &savings
	}
	report.Assumptions = sortedKeys(assumptionSet)
	report.UnknownModels = sortedKeys(unknownSet)
	return report
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EstimateFromBytes estimates cost for a given amount of text content.
// tablePath is the optional workspace price table ("" = builtin + user
// file). Cost is nil when the model's price is unknown — callers render
// "unknown", never $0.
func EstimateFromBytes(contentBytes int, provider string, model string, priceOverride float64, tablePath string) (inputTokens int, cost *decimal.Decimal, err error) {
	ct, err := NewCostTrackerWithTable(provider, priceOverride, tablePath)
	if err != nil {
		return 0, nil, err
	}
	return estimateFromBytesWithRegistry(contentBytes, provider, model, priceOverride, ct.registry)
}

// estimateFromBytesWithRegistry is the hermetic test seam — same math with
// an explicit registry (no filesystem reads).
func estimateFromBytesWithRegistry(contentBytes int, provider, model string, priceOverride float64, r *Registry) (int, *decimal.Decimal, error) {
	inputTokens := contentBytes / 4 // ~4 chars per token heuristic
	ct := newCostTrackerWithRegistry(provider, priceOverride, r)
	price, ok := ct.priceFor(model)
	if !ok || price.InputPerMTok == nil || price.OutputPerMTok == nil {
		return inputTokens, nil, nil
	}

	// Estimate: input + ~25% output overhead
	outputTokens := inputTokens / 4
	c := price.InputPerMTok.Mul(decimal.NewFromInt(int64(inputTokens))).Div(million)
	c = c.Add(price.OutputPerMTok.Mul(decimal.NewFromInt(int64(outputTokens))).Div(million))
	return inputTokens, &c, nil
}

// CostEstimates is the standard/batch/cached estimate triplet for
// `compile --estimate`. Batch and Cached are nil when the registry lacks
// the corresponding rates for the model — callers omit the line rather
// than invent a multiplier.
type CostEstimates struct {
	InputTokens int
	Standard    *decimal.Decimal
	Batch       *decimal.Decimal
	Cached      *decimal.Decimal
}

// EstimateVariantsFromBytes computes the estimate triplet from actual
// registry rates (never hard-coded discounts).
func EstimateVariantsFromBytes(contentBytes int, provider string, model string, priceOverride float64, tablePath string) (CostEstimates, error) {
	ct, err := NewCostTrackerWithTable(provider, priceOverride, tablePath)
	if err != nil {
		return CostEstimates{}, err
	}
	return estimateVariantsWithRegistry(contentBytes, provider, model, priceOverride, ct.registry), nil
}

// estimateVariantsWithRegistry is the hermetic test seam for
// EstimateVariantsFromBytes.
func estimateVariantsWithRegistry(contentBytes int, provider, model string, priceOverride float64, r *Registry) CostEstimates {
	var out CostEstimates
	out.InputTokens = contentBytes / 4
	ct := newCostTrackerWithRegistry(provider, priceOverride, r)
	price, ok := ct.priceFor(model)
	if !ok || price.InputPerMTok == nil || price.OutputPerMTok == nil {
		return out // unknown model: all variants nil
	}
	outputTokens := out.InputTokens / 4
	in := decimal.NewFromInt(int64(out.InputTokens))
	outTokens := decimal.NewFromInt(int64(outputTokens))

	standard := price.InputPerMTok.Mul(in).Div(million).Add(price.OutputPerMTok.Mul(outTokens).Div(million))
	out.Standard = &standard

	if price.BatchInputPerMTok != nil && price.BatchOutputPerMTok != nil {
		batch := price.BatchInputPerMTok.Mul(in).Div(million).Add(price.BatchOutputPerMTok.Mul(outTokens).Div(million))
		out.Batch = &batch
	}
	if price.CachedInputPerMTok != nil {
		cached := price.CachedInputPerMTok.Mul(in).Div(million).Add(price.OutputPerMTok.Mul(outTokens).Div(million))
		out.Cached = &cached
	}
	return out
}

// FormatReport returns a human-readable cost summary. Unknown cost renders
// as "unknown (model not in price registry)" — never $0.0000.
func FormatReport(r *CostReport) string {
	var b strings.Builder
	b.WriteString("\n💰 Cost report (approximate)\n")
	b.WriteString(fmt.Sprintf("   Tokens: %d input, %d output", r.TotalInputTokens, r.TotalOutputTokens))
	if r.TotalCachedTokens > 0 {
		b.WriteString(fmt.Sprintf(" (%d cached)", r.TotalCachedTokens))
	}
	b.WriteString("\n")
	if r.Cost == nil {
		b.WriteString(fmt.Sprintf("   Cost:   unknown (model not in price registry: %s)\n", strings.Join(r.UnknownModels, ", ")))
	} else {
		b.WriteString(fmt.Sprintf("   Cost:   ~$%s", r.Cost.StringFixed(4)))
		if r.CacheSavings != nil && r.CacheSavings.GreaterThan(decimal.Zero) {
			b.WriteString(fmt.Sprintf(" (saved ~$%s from caching)", r.CacheSavings.StringFixed(4)))
		}
		b.WriteString("\n")
	}

	if len(r.PerPass) > 1 {
		for _, pass := range sortedPassNames(r.PerPass) {
			pc := r.PerPass[pass]
			costStr := "unknown"
			if pc.Cost != nil {
				costStr = "~$" + pc.Cost.StringFixed(4)
			}
			b.WriteString(fmt.Sprintf("   ├─ %s: %d calls, %d tokens, %s\n",
				pass, pc.Calls, pc.InputTokens+pc.OutputTokens, costStr))
		}
	}

	return b.String()
}

func sortedPassNames(perPass map[string]PassCost) []string {
	names := make([]string, 0, len(perPass))
	for name := range perPass {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
