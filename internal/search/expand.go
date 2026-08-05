package search

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
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
// expandTimeout bounds one expansion call. Expansion is an optimization —
// a slow provider must not hold a search open for the transport's 120s.
const expandTimeout = 20 * time.Second

// ExpandQuery is bounded by expandTimeout and by the caller's context: an
// MCP client that cancels wiki_search{expand:true}, or a Ctrl-C on the CLI,
// must actually stop the call rather than wait out the transport timeout.
func ExpandQuery(ctx context.Context, question string, client *llm.Client, model string) (*ExpandedQuery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, expandTimeout)
	defer cancel()

	// SPEC-08 D4: the user query is untrusted input meeting instructions —
	// it enters inside the canonical untrusted frame (P1-6), instructions
	// stay outside.
	prompt := "Given the search query below, generate search variants to improve retrieval:\n" +
		prompts.WrapUntrusted(question) + `
Generate search variants to improve retrieval:
- lex: 2 keyword-rich rewrites (for full-text search, use technical terms)
- vec: 1 natural language rewrite (for semantic vector search)
- hyde: 1 hypothetical answer sentence (what a good answer might say)

Respond ONLY with JSON, no explanation:
{"lex":["...","..."],"vec":["..."],"hyde":"..."}`

	// P2-4: schema-guaranteed JSON where supported; graceful degrade to
	// fallbackExpansion on any error — exactly today's failure contract.
	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, ExpansionSchema, llm.CallOpts{Model: model, MaxTokens: 300})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err // never swallow cancellation into the fallback
		}
		return fallbackExpansion(question), nil // degrade gracefully, don't return error
	}

	var resp expansionResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return fallbackExpansion(question), nil
	}
	expanded := &ExpandedQuery{
		Lex:  resp.Lex,
		Vec:  resp.Vec,
		Hyde: resp.Hyde,
	}
	expanded.Original = question
	return expanded, nil
}

// ExpansionSchema is the canonical schema for query expansion (P2-4) —
// object-rooted (no envelope).
var ExpansionSchema = llm.JSONSchema{
	Name:        "expansion",
	Description: "search query variants",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"lex":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"vec":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"hyde": map[string]any{"type": "string"},
		},
		"required": []string{"lex", "vec", "hyde"},
	},
}

// StrongSignal checks if the top BM25 result is confident enough to skip expansion.
// Returns true if BOTH: (a) top-1 normalized score >= 0.4, AND (b) top-1 >= 2x top-2.
// A single result above the floor is also a strong signal.
func StrongSignal(query string, memStore store.EntryStore) bool {
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
