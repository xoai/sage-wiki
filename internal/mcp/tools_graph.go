package mcp

import (
	"context"
	"encoding/json"

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
	hops := 0
	if v, ok := args["hops"].(float64); ok {
		hops = int(v)
	}
	maxEdges := 0
	if v, ok := args["max_edges"].(float64); ok {
		maxEdges = int(v)
	}

	client, err := auth.NewLLMClient(s.cfg)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	model := s.cfg.Models.Query
	if model == "" {
		model = s.cfg.Models.Write
	}

	res, err := query.GraphQA(ctx, s.ont, s.searcher, client, question, query.GraphQAOpts{
		Embedder:     s.embedder,
		Model:        model,
		BM25Weight:   s.cfg.Search.HybridWeightBM25,
		VectorWeight: s.cfg.Search.HybridWeightVector,
		GraphQuery:   s.cfg.Ontology.GraphQuery,
		Hops:         hops,
		MaxEdges:     maxEdges,
	})
	if err != nil {
		return errorResult(err.Error()), nil
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	return textResult(string(data)), nil
}
