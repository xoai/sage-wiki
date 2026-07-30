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
}

// New builds the facade router. cfg is read at request time for the
// feature-gate pre-checks (the same predicates the tools use).
func New(d Dispatcher, cfg *config.Config, projectDir string) *Router {
	r := &Router{d: d, cfg: cfg, projectDir: projectDir}
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
