package query

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/graph"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
)

// P3-4: graph-grounded QA. This file owns the config resolution for the
// wiki_graph_query surface; GraphQA itself (S2) is its only consumer, which
// is why applyGraphQueryDefaults stays package-private — the MCP handler
// passes RAW values through GraphQAOpts and never resolves defaults itself.

const (
	defaultGraphQueryMaxHops  = 2
	defaultGraphQueryMaxEdges = 60
	maxGraphQueryHops         = 5
	maxGraphQueryEdges        = 500
)

// applyGraphQueryDefaults resolves the graph_query config block. Out-of-range
// values fall BACK to the default rather than clamping — the resolve-threshold
// rationale: the only safe reading of an out-of-range value is "unset", and
// clamping a typo silently honors it.
func applyGraphQueryDefaults(c config.GraphQueryConfig) config.GraphQueryConfig {
	if c.MaxHops < 1 || c.MaxHops > maxGraphQueryHops {
		c.MaxHops = defaultGraphQueryMaxHops
	}
	if c.MaxEdges < 1 || c.MaxEdges > maxGraphQueryEdges {
		c.MaxEdges = defaultGraphQueryMaxEdges
	}
	return c
}

// CitedEdge is one edge the model cited, with its provenance carried
// verbatim from the relation.
type CitedEdge struct {
	Line       string  `json:"line"`
	SourceID   string  `json:"source"`
	TargetID   string  `json:"target"`
	Relation   string  `json:"relation"`
	SourceDoc  string  `json:"source_doc,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Evidence   string  `json:"evidence,omitempty"`
	ValidFrom  string  `json:"valid_from,omitempty"`
	ValidTo    string  `json:"valid_to,omitempty"`
}

// GraphQAResult is a graph-grounded answer with edge-level citations.
type GraphQAResult struct {
	Answer    string      `json:"answer"`
	Cited     []CitedEdge `json:"cited"`
	Truncated bool        `json:"truncated"`
	Seeds     []string    `json:"seeds"`
}

// GraphQAOpts carries the caller's dependencies and bounds. GraphQA never
// loads config itself — the caller owns cfg reads and passes the
// graph_query block verbatim; a nil Embedder degrades seed search to
// BM25-only, the same rule as every other search surface.
type GraphQAOpts struct {
	Embedder     embed.Embedder
	Model        string
	BM25Weight   float64
	VectorWeight float64
	// GraphQuery is cfg.Ontology.GraphQuery, verbatim — the config knob's
	// ONE path in.
	GraphQuery config.GraphQueryConfig
	// Hops/MaxEdges are RAW per-call overrides; 0 = unset. Precedence:
	// valid arg > valid config > literal default; out-of-range falls back.
	Hops     int
	MaxEdges int
	// AsOf makes the answer point-in-time (P3-6): the subgraph contains
	// only edges live at AsOf and the prompt says so. Zero means now.
	AsOf time.Time
}

// graphQAGroundingInstruction is the system prompt. The capture test pins
// the "ONLY from the listed edges" phrase — the grounding contract is
// verified to reach the provider, not assumed.
const graphQAGroundingInstruction = "You answer questions about a private knowledge graph. " +
	"Answer ONLY from the listed edges — do not use outside knowledge. " +
	"Cite the edge numbers supporting every claim in the cited field " +
	"(e.g. cite edge E2 as the integer 2). If the listed edges do not " +
	"support an answer, say so and cite nothing."

// graphQASchema is the structured-output contract. No enums — the
// resolve-pass rationale: StructuredCompletion's fallback validates the
// same schema, and enums make the fallback brittle.
var graphQASchema = llm.JSONSchema{
	Name:        "graph_answer",
	Description: "Answer grounded in the serialized subgraph with edge-number citations",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
			"cited":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		},
		"required": []string{"answer", "cited"},
	},
}

// GraphQA answers a question by traversal: seed entities from hybrid search
// over the question, a bounded serialized subgraph, and an LLM answer that
// may only cite the listed edges.
func GraphQA(ctx context.Context, ont store.OntologyStore, searcher *hybrid.Searcher,
	client *llm.Client, question string, opts GraphQAOpts) (GraphQAResult, error) {

	resolved := applyGraphQueryDefaults(opts.GraphQuery)
	hops := resolved.MaxHops
	if opts.Hops >= 1 && opts.Hops <= maxGraphQueryHops {
		hops = opts.Hops
	}
	maxEdges := resolved.MaxEdges
	if opts.MaxEdges >= 1 && opts.MaxEdges <= maxGraphQueryEdges {
		maxEdges = opts.MaxEdges
	}

	var queryVec []float32
	if opts.Embedder != nil {
		queryVec, _ = opts.Embedder.Embed(question)
	}
	results, err := searcher.Search(hybrid.SearchOpts{
		Query:        question,
		Limit:        5,
		BM25Weight:   opts.BM25Weight,
		VectorWeight: opts.VectorWeight,
	}, queryVec)
	if err != nil {
		return GraphQAResult{}, fmt.Errorf("graphqa: seed search: %w", err)
	}
	seeds := extractSeedIDsFromDocLevel(results)
	if len(seeds) == 0 {
		// The empty short-circuit contract: no seeds, no LLM call.
		return GraphQAResult{Answer: "no graph entities matched the question", Cited: []CitedEdge{}, Seeds: []string{}}, nil
	}

	sg, err := graph.SerializeSubgraph(ont, seeds, graph.SubgraphOpts{MaxHops: hops, MaxEdges: maxEdges, AsOf: opts.AsOf})
	if err != nil {
		return GraphQAResult{}, fmt.Errorf("graphqa: %w", err)
	}
	if len(sg.Edges) == 0 {
		// With as_of set, an empty subgraph means "nothing valid then" —
		// say so; the bare message reads as "no edges ever".
		msg := "no edges found for the matched entities"
		if !opts.AsOf.IsZero() {
			msg = fmt.Sprintf("no edges valid as of %s for the matched entities", opts.AsOf.UTC().Format(time.RFC3339))
		}
		return GraphQAResult{Answer: msg, Cited: []CitedEdge{}, Seeds: sg.Seeds}, nil
	}

	// The subgraph text is built from user-document-derived names and
	// evidence — it goes through the canonical untrusted frame (P1-6),
	// whose neutralization defangs literal delimiter tags in entity names.
	var timeNote string
	if !opts.AsOf.IsZero() {
		timeNote = fmt.Sprintf(" (as of %s — only facts valid then are listed)", opts.AsOf.UTC().Format(time.RFC3339))
	}
	// SPEC-08 D4: the question joins the (already framed) subgraph text as
	// untrusted input — both inside the canonical frame (P1-6).
	user := "Question:\n" + prompts.WrapUntrusted(question) + timeNote + "\n\nGraph edges:\n" + prompts.WrapUntrusted(sg.Text)
	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: graphQAGroundingInstruction},
		{Role: "user", Content: user},
	}, graphQASchema, llm.CallOpts{Model: opts.Model, MaxTokens: 2000})
	if err != nil {
		return GraphQAResult{}, fmt.Errorf("graphqa: llm: %w", err)
	}

	var parsed struct {
		Answer string `json:"answer"`
		Cited  []int  `json:"cited"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return GraphQAResult{}, fmt.Errorf("graphqa: parse structured payload: %w", err)
	}

	res := GraphQAResult{
		Answer:    parsed.Answer,
		Cited:     []CitedEdge{},
		Truncated: sg.Truncated,
		Seeds:     sg.Seeds,
	}
	seenCite := make(map[int]bool)
	for _, n := range parsed.Cited {
		// Out-of-range citation ints are dropped — a model citing E99 of
		// 12 must not error the call or fabricate an edge.
		if n < 1 || n > len(sg.Edges) || seenCite[n] {
			continue
		}
		seenCite[n] = true
		r := sg.Edges[n-1].Rel
		res.Cited = append(res.Cited, CitedEdge{
			Line:       sg.Edges[n-1].Line,
			SourceID:   r.SourceID,
			TargetID:   r.TargetID,
			Relation:   r.Relation,
			SourceDoc:  r.SourceDoc,
			Confidence: r.Confidence,
			Evidence:   r.Evidence,
			ValidFrom:  r.ValidFrom,
			ValidTo:    r.ValidTo,
		})
	}
	return res, nil
}
