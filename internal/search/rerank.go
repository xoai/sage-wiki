package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// RerankCandidate is a search result to be re-ranked by the LLM.
type RerankCandidate struct {
	ID            string
	ChunkText     string
	RetrievalRank int // position in the pre-rerank RRF list
}

// RerankResult is a re-ranked search result. Scored distinguishes a
// genuine LLM score (including 0) from a candidate the LLM never scored —
// conflating the two is the zero-coercion failure sage-memory measured at
// 25-50pp R@1 (ADR-038).
type RerankResult struct {
	ID            string
	Score         float64 // normalized 0-1, meaningful only when Scored
	Scored        bool
	RetrievalRank int
}

const (
	maxChunkTokens  = 400
	maxPromptTokens = 8000
	maxCandidates   = 15
)

// Rerank calls the LLM to re-score candidates by relevance to the query.
// Returns results sorted by LLM score descending. On LLM failure, returns
// candidates in original order with zero scores.
// rerankTimeout bounds one rerank call. Reranking is an optimization over
// an already-usable RRF ordering, so failing back to that order beats
// holding the caller for the transport timeout.
const rerankTimeout = 30 * time.Second

// Rerank is bounded by rerankTimeout and by the caller's context (ADR-038's
// "timeout bounded per call" — the transport's 120s was the only bound
// before, which is not a bound a search surface can offer).
func Rerank(ctx context.Context, query string, candidates []RerankCandidate, client *llm.Client, model string) ([]RerankResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, rerankTimeout)
	defer cancel()

	if len(candidates) == 0 {
		return nil, nil
	}

	// Cap candidates
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	// Truncate chunks and enforce token budget
	var passages []string
	totalTokens := extract.EstimateTokens(query) + 100 // overhead for prompt frame
	for i, c := range candidates {
		text := truncateToTokens(c.ChunkText, maxChunkTokens)
		tokens := extract.EstimateTokens(text)
		if totalTokens+tokens > maxPromptTokens {
			candidates = candidates[:i]
			break
		}
		totalTokens += tokens
		passages = append(passages, fmt.Sprintf("[%d] %s", i+1, text))
	}

	if len(passages) == 0 {
		return fallbackRerank(candidates), nil
	}

	// SPEC-08 D4: both the user query and the retrieved passages are
	// untrusted input meeting instructions — each enters inside the
	// canonical untrusted frame (P1-6); instructions stay outside.
	prompt := "Rate the relevance of each passage to the query on a scale of 0-10.\nQuery:\n" +
		prompts.WrapUntrusted(query) + "\n\nPassages:\n" +
		prompts.WrapUntrusted(strings.Join(passages, "\n\n")) + `

Respond ONLY with a JSON array, no explanation:
[{"id":1,"score":7},{"id":2,"score":2},...]`

	// P2-4: schema-guaranteed JSON where supported; graceful degrade to
	// fallbackRerank on any error — today's failure contract.
	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, RerankSchema, llm.CallOpts{Model: model, MaxTokens: 500})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return fallbackRerank(candidates), nil
	}

	scores, err := parseRerankJSON(string(payload), len(candidates))
	if err != nil {
		return fallbackRerank(candidates), nil
	}

	// Build results with normalized scores; unscored candidates are
	// marked, never coerced to 0.
	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		r := RerankResult{ID: c.ID, RetrievalRank: c.RetrievalRank}
		if i < len(scores) && scores[i] != nil {
			r.Score = *scores[i] / 10.0 // normalize 0-10 → 0-1
			r.Scored = true
		}
		results[i] = r
	}

	// Sort by score descending, RetrievalRank as the deterministic
	// tiebreak (unscored sort below any scored candidate here; the blend
	// stage restores their normalized relevance).
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Scored != results[j].Scored {
			return results[i].Scored
		}
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].RetrievalRank < results[j].RetrievalRank
	})

	return results, nil
}

// fallbackRerank returns candidates in original order, all unscored —
// downstream, zero coverage means the blend never applies.
func fallbackRerank(candidates []RerankCandidate) []RerankResult {
	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		results[i] = RerankResult{
			ID:            c.ID,
			RetrievalRank: c.RetrievalRank,
		}
	}
	return results
}

// NormalizeRelevance min-max normalizes RRF scores to [0,1]. All-equal
// inputs (including a single candidate) normalize to 1.0 — order preserved,
// no division by zero.
func NormalizeRelevance(scores []float64) []float64 {
	if len(scores) == 0 {
		return nil
	}
	lo, hi := scores[0], scores[0]
	for _, s := range scores[1:] {
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	out := make([]float64, len(scores))
	if hi == lo {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}
	for i, s := range scores {
		out[i] = (s - lo) / (hi - lo)
	}
	return out
}

// BlendReranked computes final scores in normalized [0,1] space.
// rels is indexed by RetrievalRank-1. If the LLM scored fewer than
// minCoverage of the candidates, the blend is skipped entirely (returns
// applied=false) and callers keep pure RRF order — a no-op beats a silent
// partial-coverage regression (ADR-038). Unscored candidates keep their
// normalized relevance untouched.
func BlendReranked(rels []float64, reranked []RerankResult, minCoverage float64) ([]float64, bool) {
	if len(reranked) == 0 || len(rels) == 0 {
		return nil, false
	}
	scored := 0
	for _, rr := range reranked {
		if rr.Scored {
			scored++
		}
	}
	if float64(scored)/float64(len(reranked)) < minCoverage {
		return nil, false
	}

	finals := make([]float64, len(rels))
	copy(finals, rels)
	for _, rr := range reranked {
		idx := rr.RetrievalRank - 1
		if idx < 0 || idx >= len(finals) {
			continue
		}
		if rr.Scored {
			finals[idx] = BlendScore(rels[idx], rr.Score, rr.RetrievalRank)
		}
	}
	return finals, true
}

// RerankSchema is the canonical schema for rerank (P2-4). minItems: 1 —
// an empty entries list is the model's silent-failure mode (today it
// zeroes every score unnoticed).
var RerankSchema = llm.JSONSchema{
	Name:        "rerank",
	Description: "relevance scores for candidate passages",
	IsArray:     true,
	Schema: map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "integer"},
				"score": map[string]any{"type": "number"},
			},
			"required": []string{"id", "score"},
		},
		"minItems": 1,
	},
}

// rerankEntry matches the JSON schema from the LLM.
type rerankEntry struct {
	ID    int     `json:"id"`
	Score float64 `json:"score"`
}

// parseRerankJSON extracts scores from LLM rerank response.
// Returns a slice indexed by candidate position (0-based); nil entries are
// candidates the LLM did not score — distinct from a genuine 0 score.
func parseRerankJSON(text string, numCandidates int) ([]*float64, error) {
	text = strings.TrimSpace(text)

	// Strip code fences
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		text = strings.Join(jsonLines, "\n")
	}

	// Find JSON array
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var entries []rerankEntry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return nil, err
	}

	scores := make([]*float64, numCandidates)
	for _, e := range entries {
		idx := e.ID - 1 // LLM uses 1-based IDs
		if idx >= 0 && idx < numCandidates {
			s := e.Score
			scores[idx] = &s
		}
	}
	return scores, nil
}

// BlendScore computes the final score by blending retrieval (RRF) and rerank scores.
// The weight depends on the item's retrieval rank (pre-rerank position):
//   - Ranks 1-3:  75% retrieval, 25% reranker
//   - Ranks 4-10: 60% retrieval, 40% reranker
//   - Ranks 11+:  40% retrieval, 60% reranker
func BlendScore(rrfScore, rerankScore float64, retrievalRank int) float64 {
	var rw, rew float64
	switch {
	case retrievalRank <= 3:
		rw, rew = 0.75, 0.25
	case retrievalRank <= 10:
		rw, rew = 0.60, 0.40
	default:
		rw, rew = 0.40, 0.60
	}
	return rw*rrfScore + rew*rerankScore
}

// truncateToTokens truncates text to approximately maxTokens.
func truncateToTokens(text string, maxTokens int) string {
	tokens := extract.EstimateTokens(text)
	if tokens <= maxTokens {
		return text
	}
	// Rough truncation: estimate chars per token and cut
	ratio := float64(len(text)) / float64(tokens)
	maxChars := int(float64(maxTokens) * ratio)
	if maxChars >= len(text) {
		return text
	}
	return text[:maxChars] + "..."
}
