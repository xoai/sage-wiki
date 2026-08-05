package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"

	"github.com/xoai/sage-wiki/pkg/events"
	"github.com/xoai/sage-wiki/pkg/provider"
)

// SearchRequest mirrors the unified retrieval request.
type SearchRequest struct {
	Query      string
	Limit      int
	Channels   []string // "bm25" | "vector" | "graph"; nil = all available
	Expand     bool
	Rerank     bool
	Tags       []string // soft boost
	FilterTags []string // hard AND filter
	// Granularity: "docs" (default) or "chunks".
	Granularity string
}

// SearchResult is one hit. The JSON tags match the CLI's DocResult wire
// shape byte-for-byte so `search --format json` output is unchanged;
// engine-only fields are excluded from JSON.
type SearchResult struct {
	DocID       string   `json:"ID"`
	Text        string   `json:"Content"`
	Tags        []string `json:"Tags,omitempty"`
	ArticlePath string   `json:"ArticlePath"`
	BM25Rank    int      `json:"BM25Rank"`
	VectorRank  int      `json:"VectorRank"`
	GraphRank   int      `json:"GraphRank,omitempty"`
	RRFScore    float64  `json:"RRFScore"`
	Score       float64  `json:"FinalScore"`
	SourceDate  int64    `json:"SourceDate,omitempty"`
	AliasOf     string   `json:"AliasOf,omitempty"`

	ChunkID string `json:"-"`
	Heading string `json:"-"`
	Rank    int    `json:"-"`
}

// SearchResults is the engine's search output.
type SearchResults struct {
	Results []SearchResult
}

// Search runs the unified retrieval pipeline over the workspace.
func (w *Workspace) Search(ctx context.Context, req SearchRequest) (*SearchResults, error) {
	ctx = orBackground(ctx)
	searchStart := time.Now()
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.checkOpen(); err != nil {
		return nil, err
	}

	// SPEC-08 D1: overlong queries fail fast before any provider call
	// (no cost incurred) — typed error + limit_exceeded event.
	lim := w.ResolvedLimits()
	if int64(len(req.Query)) > lim.MaxQueryBytes {
		le := limits.New(limits.WhichQueryBytes, lim.MaxQueryBytes, int64(len(req.Query)))
		w.emitLimitExceeded(le, "search")
		return nil, fmt.Errorf("engine: search: %w", le)
	}

	cfg := w.app.Config
	deps := search.Deps{
		Mem:                  w.app.Mem,
		Chunks:               w.app.Backend.Chunks(),
		Vec:                  w.app.Vec,
		Embedder:             w.searchEmbedder(ctx),
		Ont:                  w.app.Ont,
		Model:                cfg.Models.Query,
		BM25Weight:           cfg.Search.HybridWeightBM25,
		VectorWeight:         cfg.Search.HybridWeightVector,
		GraphWeight:          cfg.Search.HybridWeightGraph,
		GraphRelationWeights: cfg.Search.GraphRelationWeights,
	}
	// Trust predicate, same construction as the CLI path (query/search
	// adapters): mode from config, store over the workspace DB.
	trustMode := cfg.Trust.IncludeOutputsMode()
	var trustStore *trust.Store
	if trustMode == "verified" {
		trustStore = trust.NewStore(w.app.DB)
	}
	deps.IncludeDoc = trust.IncludePredicate(trustMode, trustStore)
	if deps.Model == "" {
		deps.Model = cfg.Models.Write
	}

	// LLM stages are config-driven like every other entry point; the
	// recorder matches the CLI/MCP wiring (SPEC-05).
	if req.Expand || req.Rerank {
		client, err := auth.NewLLMClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("engine: expand/rerank need an LLM client: %w", err)
		}
		client.SetRecorder(w.usageRecorder())
		client.SetPass("expand")
		client.SetPriceOverride(cfg.Compiler.TokenPriceOverride)
		client.SetPriceTable(cfg.Compiler.PriceTable)
		deps.Client = client
	}

	sreq := search.Request{
		Query:      req.Query,
		Limit:      req.Limit,
		Expand:     req.Expand,
		Rerank:     req.Rerank,
		Tags:       req.Tags,
		FilterTags: req.FilterTags,
	}
	if len(req.Channels) > 0 {
		parsed, unknown := search.ParseChannels(req.Channels)
		if len(unknown) > 0 {
			return nil, fmt.Errorf("engine: unknown channels: %v (valid: bm25, vector, graph)", unknown)
		}
		sreq.Channels = parsed
	}
	if req.Granularity == "chunks" {
		sreq.Granularity = search.Chunks
	} else {
		sreq.Granularity = search.Docs
	}
	sreq.RerankMinCoverage = cfg.Search.RerankMinCoverageOrDefault()

	resp, err := search.Run(ctx, deps, sreq)
	if err != nil {
		return nil, fmt.Errorf("engine: search: %w", err)
	}
	// Map through the adapters' DocResult shape so the public result —
	// and its JSON form — matches the CLI's wire contract exactly.
	docs := search.DocResults(resp.Results)
	out := &SearchResults{Results: make([]SearchResult, 0, len(docs))}
	for _, d := range docs {
		out.Results = append(out.Results, SearchResult{
			DocID: d.ID, Text: d.Content, Tags: d.Tags, ArticlePath: d.ArticlePath,
			BM25Rank: d.BM25Rank, VectorRank: d.VectorRank, GraphRank: d.GraphRank,
			RRFScore: d.RRFScore, Score: d.FinalScore, SourceDate: d.SourceDate,
			AliasOf: d.AliasOf,
		})
	}
	w.emitSearchPerformed(req, resp, time.Since(searchStart))
	return out, nil
}

// emitSearchPerformed fans one search_performed event into the installed
// sink (SPEC-07). Privacy default: the query is hashed — raw text enters
// the stream only under the events.raw_queries opt-in (local debug).
// Channels report the legs that actually ran (Run reports them).
func (w *Workspace) emitSearchPerformed(req SearchRequest, resp search.Response, d time.Duration) {
	if w.opts.sink == nil {
		return
	}
	// SPEC-07: the payload lists the legs that ACTUALLY ran (Run reports
	// them) — a requested channel turned off at runtime (no embedder,
	// empty ontology) must not appear.
	channels := make([]string, 0, len(resp.Channels))
	for _, c := range resp.Channels {
		channels = append(channels, string(c))
	}
	payload := events.SearchPerformed{
		QueryHash:   queryHash(req.Query),
		Channels:    channels,
		ResultCount: len(resp.Results),
		DurationMS:  d.Milliseconds(),
	}
	if w.app.Config.Events.RawQueries {
		payload.Query = req.Query
	}
	w.opts.sink.Emit(events.NewEvent(filepath.Base(w.dir), events.TypeSearchPerformed, payload))
}

// queryHash is the stable privacy-preserving query identifier: SHA-256 of
// the whitespace-trimmed query.
func queryHash(query string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(strings.TrimSpace(query))))
}

// searchEmbedder prefers an injected pkg/provider.Provider (adapted) and
// falls back to the workspace's configured embedder. The ctx threads
// search cancellation into the provider (F-057).
func (w *Workspace) searchEmbedder(ctx context.Context) embed.Embedder {
	if w.opts.provider != nil {
		return &providerEmbedder{p: w.opts.provider, ctx: ctx, w: w}
	}
	return w.app.Embedder()
}

// providerEmbedder adapts a pkg/provider.Provider to the internal
// embed.Embedder interface (one text per call).
type providerEmbedder struct {
	p   provider.Provider
	ctx context.Context
	w   *Workspace // hosts the dimension-probe cache across searches
}

// Embed implements embed.Embedder.
func (a *providerEmbedder) Embed(text string) ([]float32, error) {
	vecs, err := a.p.Embed(a.ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("engine: provider returned %d embeddings for 1 text", len(vecs))
	}
	return vecs[0], nil
}

// Dimensions implements embed.Embedder by probing one embedding. The probe
// is cached on the Workspace — once per workspace, not once per Search call
// (a probe per call is a paid round-trip on a remote provider).
func (a *providerEmbedder) Dimensions() int {
	a.w.dimsOnce.Do(func() {
		vec, err := a.Embed("dimension probe")
		if err == nil {
			a.w.dims = len(vec)
		}
	})
	return a.w.dims
}

// Name implements embed.Embedder.
func (a *providerEmbedder) Name() string { return "pkg/provider" }

// --- GraphAPI ---

// Entity is one graph node.
type Entity struct {
	ID, Type, Name, Definition, ArticlePath string
}

// Relation is one graph edge with evidence and provenance.
type Relation struct {
	ID, SourceID, TargetID, Relation string
	Evidence, SourceDoc              string
	Confidence                       float64
}

// GraphFilter scopes Entities/Relations (Type "" = all; Limit 0 = store default).
type GraphFilter struct {
	Type  string
	Limit int
}

// GraphAPI is the workspace's typed graph query surface.
type GraphAPI interface {
	Entities(ctx context.Context, f GraphFilter) ([]Entity, error)
	Relations(ctx context.Context, f GraphFilter) ([]Relation, error)
	// Neighbors returns entities within depth hops (1-5).
	Neighbors(ctx context.Context, entityID string, depth int) ([]Entity, error)
	// AsOf returns a point-in-time view (P3-6 bi-temporal edges).
	AsOf(t time.Time) GraphAPI
}

type graphAPI struct {
	w    *Workspace
	asOf time.Time
}

// Graph returns the workspace's graph query surface.
func (w *Workspace) Graph() GraphAPI {
	return &graphAPI{w: w}
}

func (g *graphAPI) Entities(ctx context.Context, f GraphFilter) ([]Entity, error) {
	g.w.mu.RLock()
	defer g.w.mu.RUnlock()
	if err := g.w.checkOpen(); err != nil {
		return nil, err
	}
	// Entities carry no validity windows (only relations do, P3-6) — a
	// point-in-time entity query is undefined. Loud error, never a silent
	// current-snapshot answer (Gate 8 M1).
	if !g.asOf.IsZero() {
		return nil, fmt.Errorf("engine: AsOf is not supported for Entities (entities have no validity windows) — use AsOf for Relations and Neighbors")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ents, err := g.w.app.Ont.ListEntities(f.Type)
	if err != nil {
		return nil, fmt.Errorf("engine: graph entities: %w", err)
	}
	out := make([]Entity, 0, len(ents))
	for _, e := range ents {
		out = append(out, mapEntity(e))
	}
	// F-058: honor the filter limit (the store has no entity LIMIT).
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (g *graphAPI) Relations(ctx context.Context, f GraphFilter) ([]Relation, error) {
	g.w.mu.RLock()
	defer g.w.mu.RUnlock()
	if err := g.w.checkOpen(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = -1 // SQLite: no LIMIT
	}
	rels, err := g.w.app.Ont.ListRelations(f.Type, limit)
	if err != nil {
		return nil, fmt.Errorf("engine: graph relations: %w", err)
	}
	out := make([]Relation, 0, len(rels))
	for _, r := range rels {
		if !g.asOf.IsZero() && !liveAt(r, g.asOf) {
			continue
		}
		out = append(out, mapRelation(r))
	}
	return out, nil
}

// liveAt reports whether a relation's validity window covers t. Edges with
// no window are always valid; unparseable stamps are treated as open (the
// store's own reads make the same assumption).
func liveAt(r store.Relation, t time.Time) bool {
	if r.ValidFrom != "" {
		if vf, err := time.Parse(time.RFC3339, r.ValidFrom); err == nil && t.Before(vf) {
			return false
		}
	}
	if r.ValidTo != "" {
		if vt, err := time.Parse(time.RFC3339, r.ValidTo); err == nil && !t.Before(vt) {
			return false
		}
	}
	return true
}

func (g *graphAPI) Neighbors(ctx context.Context, entityID string, depth int) ([]Entity, error) {
	g.w.mu.RLock()
	defer g.w.mu.RUnlock()
	if err := g.w.checkOpen(); err != nil {
		return nil, err
	}
	// Clamp to the documented range (F-060).
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	// SPEC-08 AC12: cap the visited set; over-cap returns the partial
	// result with the typed error, and the engine emits limit_exceeded.
	lim := g.w.ResolvedLimits()
	ents, err := g.w.app.Ont.Traverse(entityID, store.TraverseOpts{
		Direction: store.Both,
		MaxDepth:  depth,
		AsOf:      g.asOf,
		MaxNodes:  int(lim.MaxGraphTraversalNodes),
	})
	if err != nil {
		if errors.Is(err, limits.ErrTraversalTooWide) {
			g.w.emitLimitExceeded(err, "graph:neighbors")
			out := make([]Entity, 0, len(ents))
			for _, e := range ents {
				out = append(out, mapEntity(e))
			}
			return out, fmt.Errorf("engine: graph neighbors: %w", err)
		}
		return nil, fmt.Errorf("engine: graph neighbors: %w", err)
	}
	out := make([]Entity, 0, len(ents))
	for _, e := range ents {
		out = append(out, mapEntity(e))
	}
	return out, nil
}

// AsOf returns a point-in-time view of the graph (temporal edges only —
// a zero time means "now" and is the default).
func (g *graphAPI) AsOf(t time.Time) GraphAPI {
	return &graphAPI{w: g.w, asOf: t}
}

func mapEntity(e store.Entity) Entity {
	return Entity{ID: e.ID, Type: e.Type, Name: e.Name, Definition: e.Definition, ArticlePath: e.ArticlePath}
}

func mapRelation(r store.Relation) Relation {
	return Relation{
		ID: r.ID, SourceID: r.SourceID, TargetID: r.TargetID, Relation: r.Relation,
		Evidence: r.Evidence, SourceDoc: r.SourceDoc, Confidence: r.Confidence,
	}
}
