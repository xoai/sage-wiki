package api

import (
	"net/http"

	"github.com/xoai/sage-wiki/internal/config"
)

// Route describes one registered /v1 endpoint. The table is the single
// source of truth for both registration and the drift check (rule 2-4):
// Path is the OpenAPI form, Pattern the ServeMux form, Params the declared
// argument names (rule 3: a subset of the target tool's arguments, except
// the allow-listed wiki_add_ontology split — see INT-05).
type Route struct {
	Method  string
	Pattern string // ServeMux pattern, e.g. "/v1/articles/{path...}"
	Path    string // OpenAPI path, e.g. "/v1/articles/{path}"
	Tool    string
	Params  []string
	Handler http.HandlerFunc
}

// Router wires HTTP routes to MCP tool dispatch. It holds no state of its
// own beyond construction inputs — all capability lives behind Dispatcher.
type Router struct {
	d          Dispatcher
	cfg        *config.Config
	projectDir string
	routes     []Route
	idem       *idemStore
}

// New builds the facade router. cfg is read at request time for the
// feature-gate pre-checks (the same predicates the tools use).
func New(d Dispatcher, cfg *config.Config, projectDir string) *Router {
	r := &Router{d: d, cfg: cfg, projectDir: projectDir, idem: newIdemStore()}
	r.routes = []Route{
		{"GET", "/v1/search", "/v1/search", ToolSearch,
			[]string{"query", "tags", "boost_tags", "limit", "channels", "expand", "rerank"}, r.handleSearch},
		{"GET", "/v1/articles/{path...}", "/v1/articles/{path}", ToolRead,
			[]string{"path"}, r.handleReadArticle},
		{"GET", "/v1/status", "/v1/status", ToolStatus,
			nil, r.handleStatus},
		{"GET", "/v1/ontology/{entity}/traverse", "/v1/ontology/{entity}/traverse", ToolOntologyQuery,
			[]string{"entity", "relation", "direction", "depth"}, r.handleTraverse},
		{"POST", "/v1/graph/query", "/v1/graph/query", ToolGraphQuery,
			[]string{"question", "hops", "max_edges", "as_of", "mode"}, r.handleGraphQuery},
		{"GET", "/v1/entities", "/v1/entities", ToolList,
			[]string{"type"}, r.handleEntities},
		{"GET", "/v1/provenance", "/v1/provenance", ToolProvenance,
			[]string{"source", "article"}, r.handleProvenance},
		{"GET", "/v1/compile/diff", "/v1/compile/diff", ToolCompileDiff,
			nil, r.handleCompileDiff},
		{"POST", "/v1/sources", "/v1/sources", ToolAddSource,
			[]string{"path", "type"}, r.idempotent(r.handleAddSource)},
		{"PUT", "/v1/summaries", "/v1/summaries", ToolWriteSummary,
			[]string{"source", "content", "concepts"}, r.idempotent(r.handleWriteSummary)},
		{"PUT", "/v1/articles/{concept}", "/v1/articles/{concept}", ToolWriteArticle,
			[]string{"concept", "content"}, r.idempotent(r.handleWriteArticle)},
		// INT-05 allow-listed split: both dispatch to wiki_add_ontology
		// with an argument subset; the tool keeps its combined form.
		{"POST", "/v1/ontology/entities", "/v1/ontology/entities", ToolAddOntology,
			[]string{"entity_id", "entity_type", "entity_name"}, r.idempotent(r.handleAddOntologyEntity)},
		{"POST", "/v1/ontology/relations", "/v1/ontology/relations", ToolAddOntology,
			[]string{"source_id", "target_id", "relation"}, r.idempotent(r.handleAddOntologyRelation)},
		{"POST", "/v1/learnings", "/v1/learnings", ToolLearn,
			[]string{"type", "content", "tags"}, r.idempotent(r.handleLearn)},
		{"POST", "/v1/git/commit", "/v1/git/commit", ToolCommit,
			[]string{"message"}, r.idempotent(r.handleCommit)},
		{"POST", "/v1/capture", "/v1/capture", ToolCapture,
			[]string{"content", "context", "tags"}, r.idempotent(r.handleCapture)},
	}
	return r
}

// Routes returns the registered route table (drift check introspection).
func (r *Router) Routes() []Route {
	out := make([]Route, len(r.routes))
	copy(out, r.routes)
	return out
}

// RegisterRoutes mounts every route on mux. The caller wraps the mux with
// the existing security middleware — the facade adds no auth of its own.
func (r *Router) RegisterRoutes(mux *http.ServeMux) {
	for _, rt := range r.routes {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, rt.Handler)
	}
}

// Handler returns the facade as a self-contained http.Handler for
// mounting as a subtree (WebServer mounts it at /v1/).
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	return mux
}
