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
}

// DefaultRerankMinCoverage gates blending when the LLM scored fewer than
// this fraction of the head (ADR-038; sage-memory's measured failure mode).
const DefaultRerankMinCoverage = 0.5

// fusedChunk is a chunk hit accumulated across the BM25 and vector legs.
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
	if !applied {
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

	// Step 2: Chunk-level BM25 search with all query variants
	candidateLimit := opts.Limit * 5 // fetch more for fusion
	bm25Results, err := opts.ChunkStore.SearchChunksMultiQuery(expanded.AllQueries(), candidateLimit)
	if err != nil {
		return nil, err
	}

	// Step 3: Chunk-level vector search (if embedder available)
	var vecResults []vectors.ChunkVectorResult
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

		if len(queryVecs) > 0 {
			// Always brute-force vector search to support cross-lingual queries where BM25 has zero lexical overlap
			for _, qv := range queryVecs {
				vr, err := opts.VecStore.SearchChunks(qv, candidateLimit)
				if err != nil {
					log.Warn("chunk vector search failed", "error", err)
					continue
				}
				vecResults = append(vecResults, vr...)
			}
		}
	}

	// Step 4: RRF fusion of BM25 + vector chunk results
	chunkMap := make(map[string]*fusedChunk)
	const k = 60.0

	for _, r := range bm25Results {
		fc, ok := chunkMap[r.ChunkID]
		if !ok {
			fc = &fusedChunk{
				chunkID: r.ChunkID,
				docID:   r.DocID,
				heading: r.Heading,
				content: r.Content,
			}
			chunkMap[r.ChunkID] = fc
		}
		if fc.bm25Rank == 0 || r.Rank < fc.bm25Rank {
			fc.bm25Rank = r.Rank
		}
	}

	for _, r := range vecResults {
		fc, ok := chunkMap[r.ChunkID]
		if !ok {
			fc = &fusedChunk{
				chunkID: r.ChunkID,
				docID:   r.DocID,
			}
			chunkMap[r.ChunkID] = fc
		}
		if fc.vecRank == 0 || r.Rank < fc.vecRank {
			fc.vecRank = r.Rank
		}
	}

	// Hydrate chunks found only by vector search — they entered the map
	// without heading/content and would otherwise reach the reranker (and
	// the caller) as empty passages.
	var missing []string
	for id, fc := range chunkMap {
		if fc.content == "" && fc.heading == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		meta, err := opts.ChunkStore.GetChunksMeta(missing)
		if err != nil {
			log.Warn("chunk hydration failed; vector-only hits keep empty content", "error", err)
		} else {
			for id, m := range meta {
				chunkMap[id].heading = m.Heading
				chunkMap[id].content = m.Content
			}
		}
	}

	// Compute RRF scores
	var fused []fusedChunk
	for _, fc := range chunkMap {
		if fc.bm25Rank > 0 {
			fc.rrfScore += 1.0 / (k + float64(fc.bm25Rank))
		}
		if fc.vecRank > 0 {
			fc.rrfScore += 1.0 / (k + float64(fc.vecRank))
		}
		fused = append(fused, *fc)
	}

	sort.Slice(fused, func(i, j int) bool {
		return fused[i].rrfScore > fused[j].rrfScore
	})

	// Assign retrieval ranks
	for i := range fused {
		fused[i].retrievalRank = i + 1
	}

	// Step 5: Deduplicate to document level — keep best chunk per doc
	docBest := make(map[string]*fusedChunk)
	for i := range fused {
		fc := &fused[i]
		if existing, ok := docBest[fc.docID]; !ok || fc.rrfScore > existing.rrfScore {
			docBest[fc.docID] = fc
		}
	}

	var deduped []fusedChunk
	for _, fc := range docBest {
		deduped = append(deduped, *fc)
	}
	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].rrfScore > deduped[j].rrfScore
	})

	// Assign retrieval ranks after dedup
	for i := range deduped {
		deduped[i].retrievalRank = i + 1
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

	// If no reranking or reranking produced no results, use RRF order
	if len(results) == 0 {
		for _, fc := range deduped {
			results = append(results, SearchResult{
				DocID:      fc.docID,
				ChunkID:    fc.chunkID,
				ChunkText:  fc.content,
				Heading:    fc.heading,
				RRFScore:   fc.rrfScore,
				FinalScore: fc.rrfScore,
				Rank:       fc.retrievalRank,
			})
		}
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
