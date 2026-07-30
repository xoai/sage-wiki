package api

import (
	"net/http"
	"strings"

	"github.com/xoai/sage-wiki/internal/compiler"
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
	jobs       *jobStore
	jobRunner  JobRunner
	progress   *compiler.Progress // shared hub for job progress mirroring (nil OK)
}

// New builds the facade router. cfg is read at request time for the
// feature-gate pre-checks (the same predicates the tools use). progress, when
// non-nil, is the compile Progress hub job polling mirrors (P4-2).
func New(d Dispatcher, cfg *config.Config, projectDir string, jobRunner JobRunner, progress ...*compiler.Progress) *Router {
	var hub *compiler.Progress
	if len(progress) > 0 {
		hub = progress[0]
	}
	r := &Router{d: d, cfg: cfg, projectDir: projectDir, idem: newIdemStore(), jobs: newJobStore(), jobRunner: jobRunner, progress: hub}
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
		// Params are the REST-facing names — entities deliberately presents
		// a cleaner shape ({id,type,name}) than the tool's arguments.
		{"POST", "/v1/ontology/entities", "/v1/ontology/entities", ToolAddOntology,
			[]string{"id", "type", "name"}, r.idempotent(r.handleAddOntologyEntity)},
		{"POST", "/v1/ontology/relations", "/v1/ontology/relations", ToolAddOntology,
			[]string{"source_id", "target_id", "relation"}, r.idempotent(r.handleAddOntologyRelation)},
		{"POST", "/v1/learnings", "/v1/learnings", ToolLearn,
			[]string{"type", "content", "tags"}, r.idempotent(r.handleLearn)},
		{"POST", "/v1/git/commit", "/v1/git/commit", ToolCommit,
			[]string{"message"}, r.idempotent(r.handleCommit)},
		{"POST", "/v1/capture", "/v1/capture", ToolCapture,
			[]string{"content", "context", "tags"}, r.idempotent(r.handleCapture)},
		{"POST", "/v1/jobs/{kind}", "/v1/jobs/{kind}", "",
			nil, r.handleJobSubmit},
		{"GET", "/v1/jobs/{id}", "/v1/jobs/{id}", "",
			nil, r.handleJobGet},
		{"GET", "/v1/jobs", "/v1/jobs", "",
			nil, r.handleJobList},
		{"DELETE", "/v1/jobs/{id}", "/v1/jobs/{id}", "",
			nil, r.handleJobDelete},
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
	// Every non-2xx /v1 response uses the envelope (04 §Error model) —
	// including paths that miss the route table, which would otherwise get
	// ServeMux's plain-text 404. A path that exists under another method
	// gets 405 with Allow.
	mux.HandleFunc("/v1/", r.handleUnmatched)
}

// Handler returns the facade as a self-contained http.Handler for
// mounting as a subtree (WebServer mounts it at /v1/).
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	return mux
}

// handleUnmatched is the /v1 catch-all: 404 not_found for unknown paths,
// 405 for known paths under the wrong method. Matching runs against
// rt.Pattern (the ServeMux form) so the {path...} multi-segment wildcard
// is honoured — rt.Path's OpenAPI form has no "..." marker.
func (r *Router) handleUnmatched(w http.ResponseWriter, req *http.Request) {
	var allow []string
	for _, rt := range r.routes {
		if pathMatches(rt.Pattern, req.URL.Path) && rt.Method != req.Method {
			allow = append(allow, rt.Method)
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, http.StatusMethodNotAllowed, CodeInvalidArgument, "method not allowed for this path", map[string]any{"allow": allow})
		return
	}
	writeError(w, http.StatusNotFound, CodeNotFound, "not found", nil)
}

// pathMatches compares a ServeMux-form route pattern (with {param} and a
// trailing {param...} wildcard) to a request path, segment by segment.
func pathMatches(pattern, p string) bool {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	rs := strings.Split(strings.Trim(p, "/"), "/")
	for i, seg := range ps {
		if strings.HasSuffix(seg, "...}") {
			return true // wildcard matches the rest
		}
		if i >= len(rs) {
			return false
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue // single-segment parameter
		}
		if seg != rs[i] {
			return false
		}
	}
	return len(ps) == len(rs)
}
