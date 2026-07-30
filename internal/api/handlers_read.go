package api

import (
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// Edge validation only — every check below mirrors a tool argument's
// documented shape so failures surface as precise 400s instead of
// unclassified 500s. No tool error text is ever string-matched.

func (r *Router) handleSearch(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	args := map[string]any{}
	query := q.Get("query")
	if query == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "query is required", map[string]any{"field": "query"})
		return
	}
	args["query"] = query
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "limit must be a positive integer", map[string]any{"field": "limit", "got": raw})
			return
		}
		args["limit"] = float64(n)
	}
	if raw := q.Get("channels"); raw != "" {
		for _, c := range strings.Split(raw, ",") {
			switch strings.TrimSpace(c) {
			case "bm25", "vector", "graph":
			default:
				writeError(w, http.StatusBadRequest, CodeInvalidArgument, "channels must be a comma-separated subset of bm25,vector,graph", map[string]any{"field": "channels", "got": raw})
				return
			}
		}
		args["channels"] = raw
	}
	// Comma-separated params stay comma strings — the tool parses commas.
	if raw := q.Get("tags"); raw != "" {
		args["tags"] = raw
	}
	if raw := q.Get("boost_tags"); raw != "" {
		args["boost_tags"] = raw
	}
	if q.Get("expand") == "true" {
		args["expand"] = true
	}
	if q.Get("rerank") == "true" {
		args["rerank"] = true
	}
	dispatch(req.Context(), w, r.d, ToolSearch, args, "")
}

func (r *Router) handleReadArticle(w http.ResponseWriter, req *http.Request) {
	p := req.PathValue("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "article path is required", map[string]any{"field": "path"})
		return
	}
	// REST paths are output-dir-relative; containment is enforced here
	// (P0-2 helper), and the tool re-checks against the project root as
	// defense-in-depth. A containment failure is 403, never 404.
	abs, err := pathsafe.SafeJoin(filepath.Join(r.projectDir, r.cfg.Output), p)
	if err != nil {
		writeError(w, http.StatusForbidden, CodeForbidden, "path is outside the wiki output directory", map[string]any{"field": "path"})
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "article not found", map[string]any{"path": p})
			return
		}
		log.Printf("api: stat %s: %v", p, err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "article unavailable", nil)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "path is a directory, not an article", map[string]any{"path": p})
		return
	}
	// The tool reads project-root-relative paths.
	toolPath := path.Join(r.cfg.Output, filepath.ToSlash(p))
	res := r.d.CallTool(req.Context(), ToolRead, toolRequest(ToolRead, map[string]any{"path": toolPath}))
	if res == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "wiki_read returned no result", nil)
		return
	}
	if res.IsError {
		log.Printf("api: %s failed: %s", ToolRead, resultText(res))
		writeError(w, http.StatusInternalServerError, CodeInternal, "wiki_read failed", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "content": resultText(res)})
}

func (r *Router) handleStatus(w http.ResponseWriter, req *http.Request) {
	// Structured presentation of the same underlying call handleStatus
	// makes (spec §Rationale — the one sanctioned presentation exception).
	// nil stores: GetStatus opens the DB read-only for this call.
	info, err := wiki.GetStatus(r.projectDir, nil)
	if err != nil {
		log.Printf("api: status: %v", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "status unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (r *Router) handleTraverse(w http.ResponseWriter, req *http.Request) {
	entity := req.PathValue("entity")
	if entity == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "entity is required", map[string]any{"field": "entity"})
		return
	}
	args := map[string]any{"entity": entity}
	q := req.URL.Query()
	if raw := q.Get("depth"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 5 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "depth must be between 1 and 5", map[string]any{"field": "depth", "got": raw})
			return
		}
		args["depth"] = float64(n)
	}
	if raw := q.Get("direction"); raw != "" {
		switch raw {
		case "outbound", "inbound", "both":
			args["direction"] = raw
		default:
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "direction must be one of outbound, inbound, both", map[string]any{"field": "direction", "got": raw})
			return
		}
	}
	// Optional relation filter passes through verbatim — relation types
	// are open (config-extensible), so no enum at the edge.
	if raw := q.Get("relation"); raw != "" {
		args["relation"] = raw
	}
	dispatch(req.Context(), w, r.d, ToolOntologyQuery, args, "")
}

func (r *Router) handleGraphQuery(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	question, _ := args["question"].(string)
	if question == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "question is required", map[string]any{"field": "question"})
		return
	}
	if v, ok := args["hops"]; ok {
		n, isNum := v.(float64)
		if !isNum || n != math.Trunc(n) || n < 1 || n > 5 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "hops must be an integer between 1 and 5", map[string]any{"field": "hops", "got": v})
			return
		}
	}
	if v, ok := args["max_edges"]; ok {
		n, isNum := v.(float64)
		if !isNum || n != math.Trunc(n) || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "max_edges must be an integer between 1 and 500", map[string]any{"field": "max_edges", "got": v})
			return
		}
	}
	asOf, _ := args["as_of"].(string)
	mode, _ := args["mode"].(string)
	if asOf != "" {
		if _, err := time.Parse(time.RFC3339, asOf); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "as_of must be RFC3339 (e.g. 2026-01-15T00:00:00Z)", map[string]any{"field": "as_of", "got": asOf})
			return
		}
	}
	if mode != "" && mode != "local" && mode != "global" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "mode must be 'local' or 'global'", map[string]any{"field": "mode", "got": mode})
		return
	}
	if mode == "global" && asOf != "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "as_of applies to local mode only", map[string]any{"field": "as_of"})
		return
	}
	// Feature-gate pre-checks mirror the tools' exact predicates
	// (tools_graph.go:37, :69) — config reads, never error-text matching.
	if asOf != "" && !r.cfg.Ontology.Temporal.EnabledOrDefault() {
		writeError(w, http.StatusPreconditionFailed, CodeFeatureDisabled, "as_of requires ontology.temporal.enabled (currently false)", nil)
		return
	}
	if mode == "global" && !r.cfg.Ontology.Communities.Enabled {
		writeError(w, http.StatusPreconditionFailed, CodeFeatureDisabled, "global mode requires ontology.communities.enabled (currently false)", nil)
		return
	}
	dispatch(req.Context(), w, r.d, ToolGraphQuery, args, "")
}

var entityTypes = map[string]bool{
	"concept": true, "technique": true, "source": true, "claim": true, "artifact": true,
}

func (r *Router) handleEntities(w http.ResponseWriter, req *http.Request) {
	args := map[string]any{}
	if raw := req.URL.Query().Get("type"); raw != "" {
		if !entityTypes[raw] {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "type must be one of concept, technique, source, claim, artifact", map[string]any{"field": "type", "got": raw})
			return
		}
		args["type"] = raw
	}
	dispatch(req.Context(), w, r.d, ToolList, args, "")
}

func (r *Router) handleProvenance(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	source, article := q.Get("source"), q.Get("article")
	// REST validates exactly-one-of; the tool accepts both-optional
	// (04 #7 — a DX improvement that changes no tool behaviour).
	if (source == "") == (article == "") {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "exactly one of source or article is required", nil)
		return
	}
	args := map[string]any{}
	if source != "" {
		args["source"] = source
	} else {
		args["article"] = article
	}
	dispatch(req.Context(), w, r.d, ToolProvenance, args, "")
}

func (r *Router) handleCompileDiff(w http.ResponseWriter, req *http.Request) {
	dispatch(req.Context(), w, r.d, ToolCompileDiff, nil, "diff")
}
