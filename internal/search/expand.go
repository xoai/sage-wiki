package search

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/memory"
)

// ExpandedQuery holds LLM-generated query variants for enhanced search.
type ExpandedQuery struct {
	Original string   // the raw user query
	Lex      []string // keyword-rich rewrites for BM25
	Vec      []string // natural language rewrites for vector search
	Hyde     string   // hypothetical answer snippet for embedding similarity
}

// AllQueries returns Original + Lex variants for BM25 multi-query search.
func (eq *ExpandedQuery) AllQueries() []string {
	queries := []string{eq.Original}
	queries = append(queries, eq.Lex...)
	return queries
}

// ExpandQuery calls the LLM to generate search variants for a user question.
// Returns an ExpandedQuery with lex (keyword rewrites), vec (semantic rewrites),
// and hyde (hypothetical answer) variants. On any failure, returns the original
// query only — no degradation.
func ExpandQuery(question string, client *llm.Client, model string) (*ExpandedQuery, error) {
	prompt := fmt.Sprintf(`Given the search query: %q
Generate search variants to improve retrieval:
- lex: 2 keyword-rich rewrites (for full-text search, use technical terms)
- vec: 1 natural language rewrite (for semantic vector search)
- hyde: 1 hypothetical answer sentence (what a good answer might say)

Respond ONLY with JSON, no explanation:
{"lex":["...","..."],"vec":["..."],"hyde":"..."}`, question)

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: 300})
	if err != nil {
		return fallbackExpansion(question), err
	}

	expanded, err := parseExpansionJSON(resp.Content)
	if err != nil {
		return fallbackExpansion(question), nil // degrade gracefully, don't return error
	}
	expanded.Original = question
	return expanded, nil
}

// StrongSignal checks if the top BM25 result is confident enough to skip expansion.
// Returns true if BOTH: (a) top-1 normalized score >= 0.4, AND (b) top-1 >= 2x top-2.
// A single result above the floor is also a strong signal.
func StrongSignal(query string, memStore *memory.Store) bool {
	results, err := memStore.Search(query, nil, 2)
	if err != nil || len(results) == 0 {
		return false
	}

	// Normalize BM25: |score| / (1 + |score|) → [0, 1)
	top1 := normalizeBM25(results[0].BM25Score)
	if top1 < 0.4 {
		return false
	}

	if len(results) == 1 {
		return true // single result above floor
	}

	top2 := normalizeBM25(results[1].BM25Score)
	return top1 >= 2*top2
}

// normalizeBM25 maps a BM25 score to [0, 1) via |score| / (1 + |score|).
func normalizeBM25(score float64) float64 {
	abs := math.Abs(score)
	return abs / (1 + abs)
}

// fallbackExpansion returns an ExpandedQuery with only the original query.
func fallbackExpansion(question string) *ExpandedQuery {
	return &ExpandedQuery{Original: question}
}

// expansionResponse matches the JSON schema from the LLM.
type expansionResponse struct {
	Lex  []string `json:"lex"`
	Vec  []string `json:"vec"`
	Hyde string   `json:"hyde"`
}

// parseExpansionJSON extracts expansion variants from LLM response.
// Handles: raw JSON, code-fenced JSON, preamble text before JSON.
func parseExpansionJSON(text string) (*ExpandedQuery, error) {
	text = strings.TrimSpace(text)

	// Strip markdown code fences if present
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

	// Find the JSON object
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var resp expansionResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return nil, err
	}

	return &ExpandedQuery{
		Lex:  resp.Lex,
		Vec:  resp.Vec,
		Hyde: resp.Hyde,
	}, nil
}
