package search

import (
	"math"
	"sort"
	"time"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/store"
)

// Channel identifies a retrieval leg (ADR-036; the ablation surface).
type Channel string

const (
	ChannelBM25   Channel = "bm25"
	ChannelVector Channel = "vector"
	// ChannelGraph is reserved: the graph leg fuses in M4 (ADR-037).
	ChannelGraph Channel = "graph"
)

// Granularity selects the output unit (spec §2.1).
type Granularity int

const (
	// Docs returns document-level results (MCP/CLI/web adapters, M5).
	Docs Granularity = iota
	// Chunks returns the same doc-level results with their best-chunk
	// text attached — the query context-assembly shape.
	Chunks
)

// Request configures a unified search (spec §2.1). Zero values are the
// defaults: all channels, LLM stages OFF, Docs granularity.
type Request struct {
	Query    string
	Limit    int
	Channels []Channel // nil = all available

	// Expand and Rerank are the LLM stages — default OFF on every
	// entry point; only `query` opts in from config (ADR-036).
	Expand bool
	Rerank bool

	Tags       []string // soft boost (wired in T2.1b)
	FilterTags []string // hard AND filter (wired in T2.1b)

	// Trust preservation is Deps.IncludeDoc — callers inject their trust
	// semantics as a predicate; the M5 adapters map their trust-mode
	// strings onto it there (Gate-3 F-053: no dead string field here).

	// Granularity is plumbed but not yet branched on: Docs and Chunks
	// return the same doc-level results with best-chunk text until the
	// M5 adapters shape Docs output (documented no-op, F-053).
	Granularity Granularity

	// RerankMinCoverage gates blending (0 → DefaultRerankMinCoverage).
	RerankMinCoverage float64
}

// Deps carries stores and models. Nil Embedder means no vector legs;
// nil Client means no LLM stages regardless of Request flags.
type Deps struct {
	Mem      store.EntryStore
	Chunks   store.ChunkStore
	Vec      store.VectorStore
	Embedder embed.Embedder
	Client   *llm.Client
	Model    string

	// Fusion weights (0 → DefaultBM25Weight / DefaultVectorWeight —
	// the config defaults; spec §2.2 "config weights honored everywhere").
	BM25Weight   float64
	VectorWeight float64

	// Graph channel (ADR-037). Nil Ont disables the leg entirely; an
	// empty ontology short-circuits to byte-identical 2-channel results.
	Ont                  store.OntologyStore
	GraphWeight          float64            // 0 → DefaultGraphWeight
	GraphRelationWeights map[string]float64 // per-relation overrides (config-extensible types)

	// IncludeDoc, when non-nil, is the trust predicate: results whose
	// DocID it rejects are excluded. Callers inject their trust
	// semantics (query's output-trust rules; M5 adapters likewise) —
	// the facade preserves, never reinterprets, them (spec §2.1).
	IncludeDoc func(docID string) bool
}

// Response is the unified search output.
type Response struct {
	Results []SearchResult
}

// channelEnabled reports whether c is active for the request (nil = all).
func (r Request) channelEnabled(c Channel) bool {
	if len(r.Channels) == 0 {
		return true
	}
	for _, ch := range r.Channels {
		if ch == c {
			return true
		}
	}
	return false
}

// Run executes the unified retrieval pipeline (ADR-036): enhanced chunk
// pipeline → hard tag filter → soft tag boost → trust predicate → limit.
// The request-scoped stage="total" observation is V-M5c's instrument.
func Run(deps Deps, req Request) (Response, error) {
	start := time.Now()
	defer metrics.ObserveDuration(
		metrics.HistogramNamed("search_duration_seconds", metrics.LatencyBuckets(), "stage", "total"), start)

	embedder := deps.Embedder
	if !req.channelEnabled(ChannelVector) {
		embedder = nil // vector legs off
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	// Post-pipeline filters shrink the result set, and the soft boost can
	// promote candidates from below the cut — over-fetch so the final cut
	// still fills the requested limit and boosts change membership, not
	// just order (F-048, matching the doc-level formula's pool semantics).
	pipelineLimit := limit
	if len(req.FilterTags) > 0 || len(req.Tags) > 0 || deps.IncludeDoc != nil {
		pipelineLimit = limit * 3
	}

	// Graph leg (ADR-037): built here so the pipeline stays decoupled
	// from the ontology; the empty-ontology fast path guarantees the
	// byte-identity invariant. The COUNT(*) probe scales with ontology
	// size — deliberately deferred to the V-M5c latency measurement
	// (F-076): if it registers there, swap for an EXISTS-style probe.
	var graphLeg legList
	var graphAliases map[string]string
	if req.channelEnabled(ChannelGraph) && deps.Ont != nil {
		if n, err := deps.Ont.EntityCount(""); err == nil && n > 0 {
			graphLeg, graphAliases = buildGraphLeg(deps.Ont, req.Query, limit, deps.GraphRelationWeights)
		}
	}

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:             req.Query,
		Limit:             pipelineLimit,
		Client:            deps.Client,
		Model:             deps.Model,
		Embedder:          embedder,
		ChunkStore:        deps.Chunks,
		MemStore:          deps.Mem,
		VecStore:          deps.Vec,
		QueryExpansion:    req.Expand,
		RerankEnabled:     req.Rerank,
		RerankMinCoverage: req.RerankMinCoverage,
		BM25Weight:        deps.BM25Weight,
		VectorWeight:      deps.VectorWeight,
		GraphWeight:       deps.GraphWeight,
		SkipBM25:          !req.channelEnabled(ChannelBM25),
		GraphLeg:          graphLeg,
	})
	if err != nil {
		return Response{}, err
	}

	// Alias-union seeds annotate their canonical's results (spec §2.6).
	// Hydration of graph-only docs happens INSIDE the pipeline, before
	// rerank (F-072) — no facade fetch needed here.
	if len(graphAliases) > 0 {
		for i := range results {
			if alias, ok := graphAliases[results[i].DocID]; ok {
				results[i].AliasOf = alias
			}
		}
	}

	// One entries fetch serves the tag filters AND Docs-granularity
	// output shaping (ArticlePath/Tags on results — the M5 adapters'
	// contract).
	var entriesByDoc map[string]*store.Entry
	if deps.Mem != nil && (len(req.FilterTags) > 0 || len(req.Tags) > 0 || req.Granularity == Docs) {
		entriesByDoc = fetchDocEntries(deps.Mem, results)
	}

	// Tag semantics (spec §2.1): FilterTags is a hard AND filter,
	// Tags a soft boost (+3%/matching tag, cap 15% — the same formula
	// the doc-level path documents).
	if len(req.FilterTags) > 0 || len(req.Tags) > 0 {
		entryTags := func(docID string) []string {
			if e := entriesByDoc[docID]; e != nil {
				return e.Tags
			}
			return nil
		}
		if len(req.FilterTags) > 0 {
			kept := results[:0]
			for _, r := range results {
				if hasAllTags(entryTags(r.DocID), req.FilterTags) {
					kept = append(kept, r)
				}
			}
			results = kept
		}
		if len(req.Tags) > 0 {
			for i := range results {
				n := countMatchingTags(entryTags(results[i].DocID), req.Tags)
				bonus := 0.03 * float64(n)
				if bonus > 0.15 {
					bonus = 0.15
				}
				results[i].FinalScore += bonus
			}
		}
	}

	// Docs shaping: attach the entry metadata adapters emit.
	if entriesByDoc != nil {
		for i := range results {
			if e := entriesByDoc[results[i].DocID]; e != nil {
				results[i].ArticlePath = e.ArticlePath
				results[i].Tags = e.Tags
			}
		}
	}

	// Recency (spec §2.2, ADR-039): source_date attaches to every result;
	// dated docs gain 0.05 × 2^(−age/14d) — a tie-breaker (5% cap), never
	// a driver. Dateless docs get nothing (no fallback timestamp).
	if deps.Mem != nil && len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.DocID
		}
		if dates, err := deps.Mem.GetSourceDates(ids); err != nil {
			log.Warn("source dates unavailable; recency skipped", "error", err)
		} else if len(dates) > 0 {
			now := time.Now().Unix()
			for i := range results {
				ts, ok := dates[results[i].DocID]
				if !ok {
					continue
				}
				results[i].SourceDate = ts
				ageDays := float64(now-ts) / 86400.0
				if ageDays < 0 {
					ageDays = 0
				}
				results[i].FinalScore += 0.05 * math.Exp2(-ageDays/14.0)
			}
		}
	}

	// One stable re-sort covers tag and recency bonuses together.
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].FinalScore > results[j].FinalScore
	})

	if deps.IncludeDoc != nil {
		kept := results[:0]
		for _, r := range results {
			if deps.IncludeDoc(r.DocID) {
				kept = append(kept, r)
			}
		}
		results = kept
	}

	if len(results) > limit {
		results = results[:limit]
	}
	for i := range results {
		results[i].Rank = i + 1
	}
	return Response{Results: results}, nil
}

// fetchDocEntries loads the entry for each distinct result doc — one
// fetch serving tag filters, boosts, and Docs-granularity shaping.
// Lookup errors are logged (once, with a count) — a hard filter that
// excludes a doc because its lookup FAILED must not be indistinguishable
// from one that excluded an untagged doc (F-050; failure stays closed).
func fetchDocEntries(mem store.EntryStore, results []SearchResult) map[string]*store.Entry {
	ids := make([]string, 0, len(results))
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		if seen[r.DocID] {
			continue
		}
		seen[r.DocID] = true
		ids = append(ids, r.DocID)
	}
	out, err := mem.GetMany(ids)
	if err != nil {
		// One failed batch is not a reason to drop every doc: hard filters
		// treat an unhydrated doc as unmatched, so an empty map degrades
		// loudly (warning + no results) rather than silently mis-filtering.
		log.Warn("entry lookup failed for result docs — hard filters treat them as unmatched",
			"docs", len(ids), "error", err)
		return map[string]*store.Entry{}
	}
	return out
}

func hasAllTags(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func countMatchingTags(have, want []string) int {
	n := 0
	for _, w := range want {
		for _, h := range have {
			if h == w {
				n++
				break
			}
		}
	}
	return n
}
