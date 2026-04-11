package search

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/llm"
)

// RerankCandidate is a search result to be re-ranked by the LLM.
type RerankCandidate struct {
	ID            string
	ChunkText     string
	RetrievalRank int // position in the pre-rerank RRF list
}

// RerankResult is a re-ranked search result.
type RerankResult struct {
	ID            string
	Score         float64 // normalized 0-1
	RetrievalRank int
}

const (
	maxChunkTokens    = 400
	maxPromptTokens   = 8000
	maxCandidates     = 15
)

// Rerank calls the LLM to re-score candidates by relevance to the query.
// Returns results sorted by LLM score descending. On LLM failure, returns
// candidates in original order with zero scores.
func Rerank(query string, candidates []RerankCandidate, client *llm.Client, model string) ([]RerankResult, error) {
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

	prompt := fmt.Sprintf(`Rate the relevance of each passage to the query on a scale of 0-10.
Query: "%s"

%s

Respond ONLY with a JSON array, no explanation:
[{"id":1,"score":7},{"id":2,"score":2},...]`, query, strings.Join(passages, "\n\n"))

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: 500})
	if err != nil {
		return fallbackRerank(candidates), err
	}

	scores, err := parseRerankJSON(resp.Content, len(candidates))
	if err != nil {
		return fallbackRerank(candidates), nil
	}

	// Build results with normalized scores
	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		score := 0.0
		if i < len(scores) {
			score = scores[i] / 10.0 // normalize 0-10 → 0-1
		}
		results[i] = RerankResult{
			ID:            c.ID,
			Score:         score,
			RetrievalRank: c.RetrievalRank,
		}
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
}

// fallbackRerank returns candidates in original order with zero scores.
func fallbackRerank(candidates []RerankCandidate) []RerankResult {
	results := make([]RerankResult, len(candidates))
	for i, c := range candidates {
		results[i] = RerankResult{
			ID:            c.ID,
			Score:         0,
			RetrievalRank: c.RetrievalRank,
		}
	}
	return results
}

// rerankEntry matches the JSON schema from the LLM.
type rerankEntry struct {
	ID    int     `json:"id"`
	Score float64 `json:"score"`
}

// parseRerankJSON extracts scores from LLM rerank response.
// Returns a slice of scores indexed by candidate position (0-based).
func parseRerankJSON(text string, numCandidates int) ([]float64, error) {
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

	scores := make([]float64, numCandidates)
	for _, e := range entries {
		idx := e.ID - 1 // LLM uses 1-based IDs
		if idx >= 0 && idx < numCandidates {
			scores[idx] = e.Score
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
