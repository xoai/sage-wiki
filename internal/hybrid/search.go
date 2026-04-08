package hybrid

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

const rrfK = 60 // Standard RRF constant (Cormack et al. 2009)

// SearchOpts configures a hybrid search.
type SearchOpts struct {
	Query        string
	Tags         []string              // AND pre-filter
	BoostTags    []string              // soft post-ranking boost
	Limit        int
	Timestamps   map[string]time.Time  // optional: ID → last_updated for recency decay
	BM25Weight   float64               // RRF weight for BM25 results (default 1.0)
	VectorWeight float64               // RRF weight for vector results (default 1.0)
}

// SearchResult represents a hybrid search result.
type SearchResult struct {
	ID          string
	Content     string
	Tags        []string
	ArticlePath string
	BM25Rank    int
	VectorRank  int
	RRFScore    float64
}

// Searcher performs hybrid search combining BM25 and vector results.
type Searcher struct {
	memory  *memory.Store
	vectors *vectors.Store
}

// NewSearcher creates a hybrid searcher.
func NewSearcher(mem *memory.Store, vec *vectors.Store) *Searcher {
	return &Searcher{memory: mem, vectors: vec}
}

// Search performs RRF-fused hybrid search.
// If queryVec is nil, falls back to BM25-only.
func (s *Searcher) Search(opts SearchOpts, queryVec []float32) ([]SearchResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	// Fetch more candidates than limit for better RRF fusion
	candidateLimit := opts.Limit * 3

	// BM25 search
	bm25Results, err := s.memory.Search(opts.Query, opts.Tags, candidateLimit)
	if err != nil {
		return nil, err
	}

	// Vector search (if embedding available)
	var vecResults []vectors.VectorResult
	if queryVec != nil {
		vecResults, err = s.vectors.Search(queryVec, candidateLimit)
		if err != nil {
			return nil, err
		}
	}

	// Build RRF fusion
	scores := make(map[string]*fusionEntry)

	for _, r := range bm25Results {
		entry := getOrCreate(scores, r.ID, r.Content, r.Tags, r.ArticlePath)
		entry.bm25Rank = r.Rank
	}

	for _, r := range vecResults {
		entry, ok := scores[r.ID]
		if !ok {
			// Vector-only hit — we don't have content/tags, look it up
			memEntry, err := s.memory.Get(r.ID)
			if err != nil || memEntry == nil {
				continue
			}
			entry = getOrCreate(scores, r.ID, memEntry.Content, memEntry.Tags, memEntry.ArticlePath)
		}
		entry.vectorRank = r.Rank
	}

	// Calculate RRF scores with boosts
	var results []SearchResult
	for _, entry := range scores {
		bm25W := opts.BM25Weight
		if bm25W <= 0 {
			bm25W = 1.0
		}
		vecW := opts.VectorWeight
		if vecW <= 0 {
			vecW = 1.0
		}

		score := 0.0
		if entry.bm25Rank > 0 {
			score += bm25W / float64(rrfK+entry.bm25Rank)
		}
		if entry.vectorRank > 0 {
			score += vecW / float64(rrfK+entry.vectorRank)
		}

		// Tag boost: +3% per matching boost tag, cap 15%
		score += tagBoost(entry.tags, opts.BoostTags)

		// Recency decay: 14-day half-life, max +5%
		if ts, ok := opts.Timestamps[entry.id]; ok {
			score += recencyBoost(ts)
		}

		results = append(results, SearchResult{
			ID:          entry.id,
			Content:     entry.content,
			Tags:        entry.tags,
			ArticlePath: entry.articlePath,
			BM25Rank:    entry.bm25Rank,
			VectorRank:  entry.vectorRank,
			RRFScore:    score,
		})
	}

	// Sort by RRF score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].RRFScore > results[j].RRFScore
	})

	if len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

type fusionEntry struct {
	id          string
	content     string
	tags        []string
	articlePath string
	bm25Rank    int
	vectorRank  int
}

func getOrCreate(m map[string]*fusionEntry, id, content string, tags []string, articlePath string) *fusionEntry {
	if e, ok := m[id]; ok {
		return e
	}
	e := &fusionEntry{
		id:          id,
		content:     content,
		tags:        tags,
		articlePath: articlePath,
	}
	m[id] = e
	return e
}

// recencyBoost calculates a boost based on how recently the entry was updated.
// 14-day half-life, max +5%.
func recencyBoost(updatedAt time.Time) float64 {
	age := time.Since(updatedAt).Hours() / 24 // age in days
	if age < 0 {
		age = 0
	}
	halfLife := 14.0
	// Exponential decay: boost = maxBoost * 2^(-age/halfLife)
	boost := 0.05 * math.Pow(2, -age/halfLife)
	return boost
}

// tagBoost calculates the tag boost: +3% per matching tag, capped at 15%.
func tagBoost(entryTags []string, boostTags []string) float64 {
	if len(boostTags) == 0 {
		return 0
	}
	matches := 0
	for _, bt := range boostTags {
		for _, et := range entryTags {
			if strings.EqualFold(bt, et) {
				matches++
				break
			}
		}
	}
	boost := float64(matches) * 0.03
	if boost > 0.15 {
		boost = 0.15
	}
	return boost
}
