package llm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
)

// ModelPrice holds per-million-token pricing for a model.
type ModelPrice struct {
	Input       float64 // $ per 1M input tokens
	Output      float64 // $ per 1M output tokens
	CachedInput float64 // $ per 1M cached input tokens
	BatchInput  float64 // $ per 1M batch input tokens (0 = not supported)
	BatchOutput float64 // $ per 1M batch output tokens
}

// Built-in approximate prices (may go stale — shown as estimates).
var prices = map[string]map[string]ModelPrice{
	"anthropic": {
		"claude-sonnet-4-20250514":    {Input: 3.0, Output: 15.0, CachedInput: 0.3, BatchInput: 1.5, BatchOutput: 7.5},
		"claude-haiku-4-5-20251001":   {Input: 0.8, Output: 4.0, CachedInput: 0.08, BatchInput: 0.4, BatchOutput: 2.0},
		"claude-opus-4-6":             {Input: 15.0, Output: 75.0, CachedInput: 1.5, BatchInput: 7.5, BatchOutput: 37.5},
	},
	"openai": {
		"gpt-4o":      {Input: 2.5, Output: 10.0, CachedInput: 1.25, BatchInput: 1.25, BatchOutput: 5.0},
		"gpt-4o-mini": {Input: 0.15, Output: 0.60, CachedInput: 0.075, BatchInput: 0.075, BatchOutput: 0.3},
		"o3-mini":     {Input: 1.10, Output: 4.40, CachedInput: 0.55, BatchInput: 0.55, BatchOutput: 2.2},
	},
	"gemini": {
		"gemini-2.5-flash":         {Input: 0.15, Output: 0.60, CachedInput: 0.0375},
		"gemini-2.5-pro":           {Input: 1.25, Output: 10.0, CachedInput: 0.3125},
		"gemini-2.0-flash":         {Input: 0.10, Output: 0.40, CachedInput: 0.025},
		"gemini-3-flash-preview":   {Input: 0.15, Output: 0.60, CachedInput: 0.0375},
		"gemini-3.1-flash-lite":    {Input: 0.02, Output: 0.05, CachedInput: 0.005},
	},
}

// CostEntry records token usage for a single LLM call.
type CostEntry struct {
	Pass         string // summarize, extract, write, query, lint
	Model        string
	Provider     string
	InputTokens  int
	OutputTokens int
	CachedTokens int
	BatchMode    bool
}

// CostReport summarizes total cost for a compile.
type CostReport struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCachedTokens int
	TotalTokens       int
	EstimatedCost     float64
	CacheSavings      float64
	PerPass           map[string]PassCost
}

// PassCost holds cost info for a single compiler pass.
type PassCost struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
	Cost         float64
	Calls        int
}

// CostTracker accumulates token usage across a compile session.
type CostTracker struct {
	mu       sync.Mutex
	entries  []CostEntry
	provider string
	override float64 // user config override price per 1M input tokens
}

// NewCostTracker creates a tracker for the given provider.
func NewCostTracker(provider string, priceOverride float64) *CostTracker {
	return &CostTracker{
		provider: provider,
		override: priceOverride,
	}
}

// Track records a single LLM call's usage.
func (ct *CostTracker) Track(pass string, model string, usage Usage, batch bool) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.entries = append(ct.entries, CostEntry{
		Pass:         pass,
		Model:        model,
		Provider:     ct.provider,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CachedTokens: usage.CachedTokens,
		BatchMode:    batch,
	})
}

// Report generates the cost summary.
func (ct *CostTracker) Report() *CostReport {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	report := &CostReport{
		PerPass: make(map[string]PassCost),
	}

	for _, e := range ct.entries {
		report.TotalInputTokens += e.InputTokens
		report.TotalOutputTokens += e.OutputTokens
		report.TotalCachedTokens += e.CachedTokens
		report.TotalTokens += e.InputTokens + e.OutputTokens

		price := ct.getPrice(e.Model)
		cost := ct.calculateCost(e, price)
		savings := ct.calculateSavings(e, price)

		report.EstimatedCost += cost
		report.CacheSavings += savings

		pc := report.PerPass[e.Pass]
		pc.InputTokens += e.InputTokens
		pc.OutputTokens += e.OutputTokens
		pc.CachedTokens += e.CachedTokens
		pc.Cost += cost
		pc.Calls++
		report.PerPass[e.Pass] = pc
	}

	return report
}

func (ct *CostTracker) getPrice(model string) ModelPrice {
	if ct.override > 0 {
		return ModelPrice{Input: ct.override, Output: ct.override * 3, CachedInput: ct.override * 0.1}
	}

	providerPrices, ok := prices[ct.provider]
	if !ok {
		// Try to match openai-compatible to openai prices
		if ct.provider == "openai-compatible" || ct.provider == "qwen" {
			providerPrices = prices["openai"]
		}
	}

	if providerPrices != nil {
		// Exact match
		if p, ok := providerPrices[model]; ok {
			return p
		}
		// Prefix match (for versioned models like claude-sonnet-4-20250514)
		for name, p := range providerPrices {
			if strings.HasPrefix(model, name) || strings.HasPrefix(name, model) {
				return p
			}
		}
	}

	log.Warn("unknown model pricing, using default estimate", "model", model, "provider", ct.provider)
	return ModelPrice{Input: 0.50, Output: 2.0, CachedInput: 0.05}
}

func (ct *CostTracker) calculateCost(e CostEntry, price ModelPrice) float64 {
	uncachedInput := e.InputTokens - e.CachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}

	inputCost := float64(uncachedInput) * price.Input / 1_000_000
	cachedCost := float64(e.CachedTokens) * price.CachedInput / 1_000_000
	outputCost := float64(e.OutputTokens) * price.Output / 1_000_000

	if e.BatchMode && price.BatchInput > 0 {
		inputCost = float64(uncachedInput) * price.BatchInput / 1_000_000
		outputCost = float64(e.OutputTokens) * price.BatchOutput / 1_000_000
	}

	return inputCost + cachedCost + outputCost
}

func (ct *CostTracker) calculateSavings(e CostEntry, price ModelPrice) float64 {
	if e.CachedTokens == 0 {
		return 0
	}
	// Savings = what we would have paid at full price minus what we paid at cached price
	fullCost := float64(e.CachedTokens) * price.Input / 1_000_000
	cachedCost := float64(e.CachedTokens) * price.CachedInput / 1_000_000
	return fullCost - cachedCost
}

// EstimateFromBytes estimates cost for a given amount of text content.
func EstimateFromBytes(contentBytes int, provider string, model string, priceOverride float64) (inputTokens int, cost float64) {
	inputTokens = contentBytes / 4 // ~4 chars per token heuristic

	ct := &CostTracker{provider: provider, override: priceOverride}
	price := ct.getPrice(model)

	// Estimate: input + ~25% output overhead
	outputTokens := inputTokens / 4
	cost = float64(inputTokens)*price.Input/1_000_000 + float64(outputTokens)*price.Output/1_000_000
	return inputTokens, cost
}

// FormatReport returns a human-readable cost summary.
func FormatReport(r *CostReport) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n💰 Cost report\n"))
	b.WriteString(fmt.Sprintf("   Tokens: %d input, %d output", r.TotalInputTokens, r.TotalOutputTokens))
	if r.TotalCachedTokens > 0 {
		b.WriteString(fmt.Sprintf(" (%d cached)", r.TotalCachedTokens))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("   Cost:   ~$%.4f", r.EstimatedCost))
	if r.CacheSavings > 0 {
		b.WriteString(fmt.Sprintf(" (saved ~$%.4f from caching)", r.CacheSavings))
	}
	b.WriteString("\n")

	if len(r.PerPass) > 1 {
		for pass, pc := range r.PerPass {
			b.WriteString(fmt.Sprintf("   ├─ %s: %d calls, %d tokens, ~$%.4f\n",
				pass, pc.Calls, pc.InputTokens+pc.OutputTokens, pc.Cost))
		}
	}

	return b.String()
}
