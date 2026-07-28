package search

import (
	"sort"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// EnhancedSearchOpts configures the enhanced search pipeline.
type EnhancedSearchOpts struct {
	Query          string
	Limit          int
	Client         *llm.Client
	Model          string
	Embedder       embed.Embedder
	ChunkStore     *memory.ChunkStore
	MemStore       *memory.Store
	VecStore       *vectors.Store
	QueryExpansion bool // enable query expansion
	RerankEnabled  bool // enable LLM re-ranking

	// RerankMinCoverage is the minimum fraction of candidates the LLM must
	// have scored for the blend to apply (0 → DefaultRerankMinCoverage).
	RerankMinCoverage float64

	// Fusion weights (0 → DefaultBM25Weight / DefaultVectorWeight).
	BM25Weight   float64
	VectorWeight float64
}

// DefaultRerankMinCoverage gates blending when the LLM scored fewer than
// this fraction of the head (ADR-038; sage-memory's measured failure mode).
const DefaultRerankMinCoverage = 0.5

// Default fusion weights — the config defaults (spec §2.2).
const (
	DefaultBM25Weight   = 0.7
	DefaultVectorWeight = 0.3
)

// fusedChunk is a document accumulated across fusion legs, carrying its
// best chunk's identity/text (empty chunkID = doc-leg-only hit).
type fusedChunk struct {
	chunkID       string
	docID         string
	heading       string
	content       string
	rrfScore      float64
	bm25Rank      int
	vecRank       int
	retrievalRank int // position in the post-RRF list, used for blending
}

// legHit is one row of a ranked leg list; rank = position (1-based).
// docContent is the entry-content fallback carried by doc-granularity legs.
type legHit struct {
	docID      string
	chunkID    string
	heading    string
	content    string
	docContent string
}

// legList is one (leg, query-variant) ranked list.
type legList struct {
	channel Channel
	hits    []legHit
}

// fuseLegs runs weighted RRF (k=60) over all leg lists, doc-keyed:
// score[doc] = Σ_lists w_channel/(60 + rank-in-list). Chunk lists rank a
// doc at its best chunk's position; the best-ranked chunk across all chunk
// lists supplies the doc's chunk identity/text; doc-leg-only docs fall
// back to entry content. Output is sorted deterministically and carries
// per-channel best ranks for attribution.
func fuseLegs(lists []legList, wBM25, wVec float64) []fusedChunk {
	const k = 60.0
	type acc struct {
		fusedChunk
		bestChunkRank int // best in-list rank that supplied chunk info
		docContent    string
	}
	docs := make(map[string]*acc)

	for _, l := range lists {
		w := wBM25
		if l.channel == ChannelVector {
			w = wVec
		}
		seen := make(map[string]bool, len(l.hits)) // best (first) hit per doc per list
		for i, h := range l.hits {
			rank := i + 1
			a, ok := docs[h.docID]
			if !ok {
				a = &acc{fusedChunk: fusedChunk{docID: h.docID}}
				docs[h.docID] = a
			}
			if !seen[h.docID] {
				seen[h.docID] = true
				a.rrfScore += w / (k + float64(rank))
				switch l.channel {
				case ChannelBM25:
					if a.bm25Rank == 0 || rank < a.bm25Rank {
						a.bm25Rank = rank
					}
				case ChannelVector:
					if a.vecRank == 0 || rank < a.vecRank {
						a.vecRank = rank
					}
				}
			}
			if h.chunkID != "" && (a.chunkID == "" || rank < a.bestChunkRank) {
				a.chunkID, a.bestChunkRank = h.chunkID, rank
				a.heading, a.content = h.heading, h.content
			}
			if h.docContent != "" && a.docContent == "" {
				a.docContent = h.docContent
			}
		}
	}

	fused := make([]fusedChunk, 0, len(docs))
	for _, a := range docs {
		if a.chunkID == "" && a.content == "" {
			a.content = a.docContent // doc-leg-only fallback text
		}
		fused = append(fused, a.fusedChunk)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].rrfScore != fused[j].rrfScore {
			return fused[i].rrfScore > fused[j].rrfScore
		}
		return fused[i].docID < fused[j].docID // deterministic tiebreak
	})
	for i := range fused {
		fused[i].retrievalRank = i + 1
	}
	return fused
}

// blendResults converts fused chunks into final SearchResults. When the
// LLM scored at least minCov of the reranked head, scores blend in
// normalized [0,1] space (never raw RRF vs LLM — the scale conflation
// ADR-038 closes) and results sort by blended score. Below the coverage
// gate, results keep pure RRF order — V-M1d's contract, pinned by test.
func blendResults(deduped []fusedChunk, reranked []RerankResult, minCoverage float64) []SearchResult {
	rrf := make([]float64, len(deduped))
	for i, fc := range deduped {
		rrf[i] = fc.rrfScore
	}
	rels := NormalizeRelevance(rrf)

	if minCoverage <= 0 {
		minCoverage = DefaultRerankMinCoverage
	}
	finals, applied := BlendReranked(rels, reranked, minCoverage)
	if !applied && len(reranked) > 0 {
		// Warn only when a rerank actually ran and fell below the gate —
		// the plain rerank-disabled path goes through here too.
		log.Warn("rerank coverage below threshold, keeping RRF order",
			"candidates", len(reranked), "min_coverage", minCoverage)
	}

	rerankScores := make(map[string]float64)
	for _, rr := range reranked {
		if rr.Scored {
			rerankScores[rr.ID] = rr.Score
		}
	}

	results := make([]SearchResult, 0, len(deduped))
	for i, fc := range deduped {
		final := rels[i]
		if applied {
			final = finals[i]
		}
		results = append(results, SearchResult{
			DocID:       fc.docID,
			ChunkID:     fc.chunkID,
			ChunkText:   fc.content,
			Heading:     fc.heading,
			RRFScore:    fc.rrfScore,
			RerankScore: rerankScores[fc.docID],
			FinalScore:  final,
			Rank:        fc.retrievalRank,
			BM25Rank:    fc.bm25Rank,
			VectorRank:  fc.vecRank,
		})
	}
	if applied {
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].FinalScore > results[j].FinalScore
		})
	}
	return results
}

// SearchResult represents a document-level search result from the enhanced pipeline.
type SearchResult struct {
	DocID     string
	ChunkID   string
	ChunkText string
	Heading   string
	RRFScore  float64
	// RerankScore is 0 both for a genuine LLM score of 0 and for a
	// candidate the LLM never scored — consumers must not distinguish
	// the two through this field (the Scored bit lives on RerankResult
	// inside the pipeline; ADR-038).
	RerankScore float64
	FinalScore  float64
	Rank        int

	// Per-channel ranks of the best chunk (0 = the leg did not rank it).
	// These are the per-channel attribution source (spec §2.1, T6.3).
	BM25Rank   int
	VectorRank int
}

// EnhancedSearch runs the full enhanced search pipeline:
// strong-signal check → optional expansion → chunk-level BM25+vector → RRF → dedup → optional rerank → blend.
func EnhancedSearch(opts EnhancedSearchOpts) ([]SearchResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	// Step 1: Strong-signal check — skip expansion if confident
	expanded := fallbackExpansion(opts.Query)
	if opts.QueryExpansion && opts.Client != nil {
		if !StrongSignal(opts.Query, opts.MemStore) {
			exp, err := ExpandQuery(opts.Query, opts.Client, opts.Model)
			if err != nil {
				log.Warn("query expansion failed, using raw query", "error", err)
			} else {
				expanded = exp
			}
		} else {
			log.Info("strong signal detected, skipping expansion")
		}
	}

	// Step 2: build per-(leg, variant) ranked lists (spec §2.2). Every
	// list contributes w_leg/(60+rank) per doc; a doc hit by several
	// lists accumulates them all — doc- and chunk-granularity agreement
	// is signal (ADR-036). Chunk lists rank a doc at its best chunk.
	candidateLimit := opts.Limit * 5 // fetch more for fusion

	wBM25, wVec := opts.BM25Weight, opts.VectorWeight
	if wBM25 <= 0 {
		wBM25 = DefaultBM25Weight
	}
	if wVec <= 0 {
		wVec = DefaultVectorWeight
	}

	var lists []legList
	for _, q := range expanded.AllQueries() {
		// chunk-FTS
		crs, err := opts.ChunkStore.SearchChunks(q, candidateLimit)
		if err != nil {
			return nil, err
		}
		l := legList{channel: ChannelBM25}
		for _, r := range crs {
			l.hits = append(l.hits, legHit{docID: r.DocID, chunkID: r.ChunkID, heading: r.Heading, content: r.Content})
		}
		lists = append(lists, l)

		// doc-FTS
		ers, err := opts.MemStore.Search(q, nil, candidateLimit)
		if err != nil {
			return nil, err
		}
		dl := legList{channel: ChannelBM25}
		for _, e := range ers {
			dl.hits = append(dl.hits, legHit{docID: e.ID, docContent: e.Content})
		}
		lists = append(lists, dl)
	}

	if opts.Embedder != nil {
		// Embed all vector-oriented queries: original + vec variants + hyde
		var queryVecs [][]float32
		vecQueries := []string{opts.Query}
		vecQueries = append(vecQueries, expanded.Vec...)
		if expanded.Hyde != "" {
			vecQueries = append(vecQueries, expanded.Hyde)
		}
		for _, q := range vecQueries {
			vec, err := opts.Embedder.Embed(q)
			if err != nil {
				continue
			}
			queryVecs = append(queryVecs, vec)
		}

		// Always brute-force chunk-vector search to support cross-lingual
		// queries where BM25 has zero lexical overlap.
		for _, qv := range queryVecs {
			// chunk-vec
			vr, err := opts.VecStore.SearchChunks(qv, candidateLimit)
			if err != nil {
				log.Warn("chunk vector search failed", "error", err)
			} else {
				l := legList{channel: ChannelVector}
				for _, r := range vr {
					l.hits = append(l.hits, legHit{docID: r.DocID, chunkID: r.ChunkID})
				}
				lists = append(lists, l)
			}
			// doc-vec
			dvr, err := opts.VecStore.Search(qv, candidateLimit)
			if err != nil {
				log.Warn("doc vector search failed", "error", err)
			} else {
				dl := legList{channel: ChannelVector}
				for _, r := range dvr {
					dl.hits = append(dl.hits, legHit{docID: r.ID})
				}
				lists = append(lists, dl)
			}
		}
	}

	// Step 3: weighted RRF fusion, doc-keyed.
	deduped := fuseLegs(lists, wBM25, wVec)

	// Step 4: hydrate best chunks that arrived without content (vector-only
	// chunk hits) — they would otherwise reach the reranker as empty
	// passages. Docs with no chunk at all fall back to entry content.
	var missing []string
	byChunkID := make(map[string]int)
	for i := range deduped {
		fc := &deduped[i]
		if fc.chunkID != "" && fc.content == "" && fc.heading == "" {
			missing = append(missing, fc.chunkID)
			byChunkID[fc.chunkID] = i
		}
	}
	if len(missing) > 0 {
		meta, err := opts.ChunkStore.GetChunksMeta(missing)
		if err != nil {
			log.Warn("chunk hydration failed; vector-only hits keep empty content", "error", err)
		} else {
			for id, m := range meta {
				deduped[byChunkID[id]].heading = m.Heading
				deduped[byChunkID[id]].content = m.Content
			}
		}
	}

	// Step 6: Optional LLM re-ranking
	var results []SearchResult
	if opts.RerankEnabled && opts.Client != nil && len(deduped) > 1 {
		candidates := make([]RerankCandidate, len(deduped))
		for i, fc := range deduped {
			candidates[i] = RerankCandidate{
				ID:            fc.docID,
				ChunkText:     fc.content,
				RetrievalRank: fc.retrievalRank,
			}
		}

		reranked, err := Rerank(opts.Query, candidates, opts.Client, opts.Model)
		if err != nil {
			log.Warn("reranking failed, using RRF order", "error", err)
		}

		if len(reranked) > 0 {
			results = blendResults(deduped, reranked, opts.RerankMinCoverage)
		}
	}

	// If no reranking or reranking produced no results, use RRF order.
	// blendResults with no reranked input takes the not-applied branch:
	// same normalized-[0,1] FinalScore scale as every other path (F-042).
	if len(results) == 0 {
		results = blendResults(deduped, nil, opts.RerankMinCoverage)
	}

	// Final rank assignment and limit
	for i := range results {
		results[i].Rank = i + 1
	}
	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}
