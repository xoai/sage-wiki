package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/query"
)

// handleGraphQuery implements wiki_graph_query (P3-4): multi-hop,
// provenance-cited graph QA. The handler passes RAW hops/max_edges args —
// query.GraphQA owns resolution (precedence: valid arg > valid config >
// literal default) — and hands over the Server's existing stores, searcher,
// and embedder; only the LLM client is built per-call, the tools_write
// precedent.
func (s *Server) handleGraphQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	question, _ := args["question"].(string)
	if question == "" {
		return errorResult("question is required"), nil
	}
	if err := s.checkQueryLen(question); err != nil {
		return errorResult(fmt.Sprintf("question rejected: %v", err)), nil
	}
	hops := 0
	if v, ok := args["hops"].(float64); ok {
		hops = int(v)
	}
	maxEdges := 0
	if v, ok := args["max_edges"].(float64); ok {
		maxEdges = int(v)
	}

	var asOf time.Time
	if raw, _ := args["as_of"].(string); raw != "" {
		if !s.cfg.Ontology.Temporal.EnabledOrDefault() {
			return errorResult("as_of requires ontology.temporal.enabled (currently false)"), nil
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid as_of %q: expected RFC3339 (e.g. 2026-01-15T00:00:00Z)", raw)), nil
		}
		asOf = t
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "local"
	}
	if mode != "local" && mode != "global" {
		return errorResult(fmt.Sprintf("invalid mode %q: expected 'local' or 'global'", mode)), nil
	}

	client, err := auth.NewLLMClient(s.cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	model := s.cfg.Models.Query
	if model == "" {
		model = s.cfg.Models.Write
	}

	if mode == "global" {
		if raw, _ := args["as_of"].(string); raw != "" {
			return errorResult("as_of applies to local mode only (global answers are synthesized from community summaries)"), nil
		}
		if !s.cfg.Ontology.Communities.Enabled {
			return errorResult("global mode requires ontology.communities.enabled (currently false)"), nil
		}
		cs := s.backend.Communities()
		gres, err := query.GlobalQA(ctx, cs, s.searcher, client, question, query.GlobalQAOpts{
			Model:          model,
			MaxCommunities: s.cfg.Ontology.Communities.MaxCommunitiesOrDefault(),
			MaxTokens:      s.cfg.Ontology.Communities.MaxTokensOrDefault(),
			MinMembers:     s.cfg.Ontology.Communities.MinMembersOrDefault(),
			MaxParallel:    s.cfg.Compiler.MaxParallel,
			Embedder:       s.embedder,
		})
		if err != nil {
			return errorResult(err.Error()), nil
		}
		data, _ := json.MarshalIndent(gres, "", "  ")
		return textResult(string(data)), nil
	}

	res, err := query.GraphQA(ctx, s.ont, s.searcher, client, question, query.GraphQAOpts{
		Embedder:     s.embedder,
		Model:        model,
		BM25Weight:   s.cfg.Search.HybridWeightBM25,
		VectorWeight: s.cfg.Search.HybridWeightVector,
		GraphQuery:   s.cfg.Ontology.GraphQuery,
		Hops:         hops,
		MaxEdges:     maxEdges,
		AsOf:         asOf,
	})
	if err != nil {
		return errorResult(err.Error()), nil
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	return textResult(string(data)), nil
}
