package search

import (
	"sort"
	"time"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/vectors"
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

	Tags        []string // soft boost (wired in T2.1b)
	FilterTags  []string // hard AND filter (wired in T2.1b)
	TrustFilter string   // trust semantics preserved through the facade (T2.1b)

	Granularity Granularity

	// RerankMinCoverage gates blending (0 → DefaultRerankMinCoverage).
	RerankMinCoverage float64
}

// Deps carries stores and models. Nil Embedder means no vector legs;
// nil Client means no LLM stages regardless of Request flags.
type Deps struct {
	Mem      *memory.Store
	Chunks   *memory.ChunkStore
	Vec      *vectors.Store
	Embedder embed.Embedder
	Client   *llm.Client
	Model    string

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
	// Post-pipeline filters shrink the result set — over-fetch so the
	// final cut can still fill the requested limit.
	pipelineLimit := limit
	if len(req.FilterTags) > 0 || deps.IncludeDoc != nil {
		pipelineLimit = limit * 3
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
	})
	if err != nil {
		return Response{}, err
	}

	// Tag semantics (spec §2.1): FilterTags is a hard AND filter,
	// Tags a soft boost (+3%/matching tag, cap 15% — the same formula
	// the doc-level path documents).
	if len(req.FilterTags) > 0 || len(req.Tags) > 0 {
		tagsByDoc := fetchDocTags(deps.Mem, results)
		if len(req.FilterTags) > 0 {
			kept := results[:0]
			for _, r := range results {
				if hasAllTags(tagsByDoc[r.DocID], req.FilterTags) {
					kept = append(kept, r)
				}
			}
			results = kept
		}
		if len(req.Tags) > 0 {
			for i := range results {
				n := countMatchingTags(tagsByDoc[results[i].DocID], req.Tags)
				bonus := 0.03 * float64(n)
				if bonus > 0.15 {
					bonus = 0.15
				}
				results[i].FinalScore += bonus
			}
			sort.SliceStable(results, func(i, j int) bool {
				return results[i].FinalScore > results[j].FinalScore
			})
		}
	}

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

// fetchDocTags loads entry tags for each distinct result doc.
func fetchDocTags(mem *memory.Store, results []SearchResult) map[string][]string {
	out := make(map[string][]string, len(results))
	for _, r := range results {
		if _, seen := out[r.DocID]; seen {
			continue
		}
		if e, err := mem.Get(r.DocID); err == nil && e != nil {
			out[r.DocID] = e.Tags
		}
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
