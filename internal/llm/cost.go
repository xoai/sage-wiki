package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// ModelPrice holds per-million-token pricing for a model. The json tags
// are the price-table file format (PERF-04).
type ModelPrice struct {
	Input       float64 `json:"input,omitempty"`        // $ per 1M input tokens
	Output      float64 `json:"output,omitempty"`       // $ per 1M output tokens
	CachedInput float64 `json:"cached_input,omitempty"` // $ per 1M cached input tokens
	BatchInput  float64 `json:"batch_input,omitempty"`  // $ per 1M batch input tokens (0 = not supported)
	BatchOutput float64 `json:"batch_output,omitempty"` // $ per 1M batch output tokens
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
	table    map[string]map[string]ModelPrice // merged built-ins + user table; nil = built-ins
	overlay  map[string]map[string]ModelPrice // the raw user table (nil = none) — for prefix-collision precedence
}

// priceTable returns the effective lookup map (merged or built-ins).
func (ct *CostTracker) priceTable() map[string]map[string]ModelPrice {
	if ct.table != nil {
		return ct.table
	}
	return prices
}

// NewCostTracker creates a tracker for the given provider.
func NewCostTracker(provider string, priceOverride float64) *CostTracker {
	return NewCostTrackerWithTable(provider, priceOverride, "")
}

// NewCostTrackerWithTable builds a CostTracker whose price lookup starts
// from the built-ins merged with a user price table (PERF-04): the file
// wins per provider/model entry it names; built-ins cover the rest. A
// missing/unreadable/malformed file warns and falls back to built-ins —
// the table is optional, never a failure. Explicit priceOverride still
// beats everything (see getPrice).
func NewCostTrackerWithTable(provider string, priceOverride float64, tablePath string) *CostTracker {
	table := prices
	var overlay map[string]map[string]ModelPrice
	if tablePath != "" {
		overlay = loadPriceTable(tablePath)
		table = mergePriceTables(prices, overlay)
	}
	return &CostTracker{
		provider: provider,
		override: priceOverride,
		table:    table,
		overlay:  overlay,
	}
}

// loadPriceTable reads a JSON price table (same shape as the built-in
// map). Errors degrade to nil (built-ins only) with a warning.
func loadPriceTable(path string) map[string]map[string]ModelPrice {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Warn("price table unreadable — using built-in prices", "path", path, "error", err)
		return nil
	}
	var table map[string]map[string]ModelPrice
	if err := json.Unmarshal(raw, &table); err != nil {
		log.Warn("price table malformed — using built-in prices", "path", path, "error", err)
		return nil
	}
	return table
}

// mergePriceTables returns built-ins with the user's entries overlaid per
// provider/model. Nil overlay = built-ins as-is.
func mergePriceTables(base, overlay map[string]map[string]ModelPrice) map[string]map[string]ModelPrice {
	if len(overlay) == 0 {
		return base
	}
	merged := make(map[string]map[string]ModelPrice, len(base))
	for provider, models := range base {
		cp := make(map[string]ModelPrice, len(models))
		for m, p := range models {
			cp[m] = p
		}
		merged[provider] = cp
	}
	for provider, models := range overlay {
		dst, ok := merged[provider]
		if !ok {
			dst = make(map[string]ModelPrice, len(models))
			merged[provider] = dst
		}
		for m, p := range models {
			dst[m] = p
		}
	}
	return merged
}

// Track records a single LLM call's usage.
// Also THE single token-metrics hook (P2-2): fires for sync (client.go) and
// batch (pipeline.go) paths — no second recording site exists.
func (ct *CostTracker) Track(pass string, model string, usage Usage, batch bool) {
	metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "pass", pass, "direction", "input").Add(int64(usage.InputTokens))
	metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "pass", pass, "direction", "output").Add(int64(usage.OutputTokens))
	if usage.CachedTokens > 0 {
		metrics.CounterNamed("llm_tokens_total", "provider", ct.provider, "pass", pass, "direction", "cached").Add(int64(usage.CachedTokens))
	}
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

	providerPrices, ok := ct.priceTable()[ct.provider]
	lookupProvider := ct.provider
	if !ok {
		// Try to match openai-compatible to openai prices
		if ct.provider == "openai-compatible" || ct.provider == "qwen" {
			providerPrices = ct.priceTable()["openai"]
			lookupProvider = "openai"
		}
	}

	if providerPrices != nil {
		// Exact match
		if p, ok := providerPrices[model]; ok {
			return p
		}
		// Prefix match (for versioned models like claude-sonnet-4-20250514).
		// Deterministic precedence on collision: user-table entries beat
		// built-ins (Go map order is randomized; a table entry and a
		// built-in can both prefix-match the same model).
		var builtinHit *ModelPrice
		var overlayHit *ModelPrice
		var overlayName *string
		for name, p := range providerPrices {
			if strings.HasPrefix(model, name) || strings.HasPrefix(name, model) {
				if ct.overlay != nil && ct.overlay[lookupProvider] != nil {
					if _, isOverlay := ct.overlay[lookupProvider][name]; isOverlay {
						// Overlay-vs-overlay collisions resolve
						// deterministically: longest (most specific) name
						// wins, lexical order breaks ties (map order is
						// randomized).
						if overlayHit == nil || len(name) > len(*overlayName) ||
							(len(name) == len(*overlayName) && name < *overlayName) {
							hit := p
							overlayHit = &hit
							nameCopy := name
							overlayName = &nameCopy
						}
					}
				}
				if builtinHit == nil {
					hit := p
					builtinHit = &hit
				}
			}
		}
		if overlayHit != nil {
			return *overlayHit
		}
		if builtinHit != nil {
			return *builtinHit
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
// tablePath is the optional price table (PERF-04; "" = built-ins) so
// estimate-before-compile prices identically to the final report.
func EstimateFromBytes(contentBytes int, provider string, model string, priceOverride float64, tablePath string) (inputTokens int, cost float64) {
	inputTokens = contentBytes / 4 // ~4 chars per token heuristic

	ct := NewCostTrackerWithTable(provider, priceOverride, tablePath)
	price := ct.getPrice(model)

	// Estimate: input + ~25% output overhead
	outputTokens := inputTokens / 4
	cost = float64(inputTokens)*price.Input/1_000_000 + float64(outputTokens)*price.Output/1_000_000
	return inputTokens, cost
}

// FormatReport returns a human-readable cost summary.
func FormatReport(r *CostReport) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n💰 Cost report (approximate)\n"))
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
