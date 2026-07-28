package search

import (
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/memory"
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

// Run executes the unified retrieval pipeline (ADR-036). At the 2.1a
// boundary it routes through the enhanced chunk pipeline unchanged; the
// fusion rewrite (T2.2) and the remaining legs land behind this signature.
func Run(deps Deps, req Request) (Response, error) {
	embedder := deps.Embedder
	if !req.channelEnabled(ChannelVector) {
		embedder = nil // vector legs off
	}

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:             req.Query,
		Limit:             req.Limit,
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
	return Response{Results: results}, nil
}
