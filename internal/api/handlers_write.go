package api

import (
	"net/http"
	"regexp"

	"github.com/xoai/sage-wiki/internal/pathsafe"
)

// Write handlers translate JSON bodies to tool argument maps. Idempotency
// wrapping happens in the route table (router.go), not here.

const maxCaptureBytes = 100 * 1024 // mirrors mcp maxCaptureSize (tools_write.go:369)

var conceptIDShape = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (r *Router) handleAddSource(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	p, _ := args["path"].(string)
	if p == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "path is required", map[string]any{"field": "path"})
		return
	}
	if raw, _ := args["type"].(string); raw != "" {
		switch raw {
		case "article", "paper", "code":
		default:
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "type must be one of article, paper, code", map[string]any{"field": "type", "got": raw})
			return
		}
	}
	// Containment against the project root (P0-2 helper); the tool
	// re-checks as defense-in-depth. A traversal attempt is 403.
	if _, err := pathsafe.SafeJoin(r.projectDir, p); err != nil {
		writeError(w, http.StatusForbidden, CodeForbidden, "path is outside the project directory", map[string]any{"field": "path"})
		return
	}
	dispatch(req.Context(), w, r.d, ToolAddSource, args, "result")
}

func (r *Router) handleWriteSummary(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	source, _ := args["source"].(string)
	content, _ := args["content"].(string)
	if source == "" || content == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "source and content are required", nil)
		return
	}
	dispatch(req.Context(), w, r.d, ToolWriteSummary, args, "result")
}

func (r *Router) handleWriteArticle(w http.ResponseWriter, req *http.Request) {
	concept := req.PathValue("concept")
	if !conceptIDShape.MatchString(concept) {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "concept must be lowercase-hyphenated (e.g. self-attention)", map[string]any{"field": "concept", "got": concept})
		return
	}
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	content, _ := args["content"].(string)
	if content == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "content is required", map[string]any{"field": "content"})
		return
	}
	dispatch(req.Context(), w, r.d, ToolWriteArticle, map[string]any{
		"concept": concept,
		"content": content,
	}, "result")
}

// handleAddOntologyEntity and handleAddOntologyRelation are the one
// sanctioned place REST presents a cleaner shape than the tool (INT-05):
// both dispatch to wiki_add_ontology with the corresponding argument
// subset; the MCP tool keeps accepting the combined form unchanged.
func (r *Router) handleAddOntologyEntity(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	id, _ := args["id"].(string)
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "id is required", map[string]any{"field": "id"})
		return
	}
	toolArgs := map[string]any{"entity_id": id}
	if v, _ := args["type"].(string); v != "" {
		toolArgs["entity_type"] = v
	}
	if v, _ := args["name"].(string); v != "" {
		toolArgs["entity_name"] = v
	}
	dispatch(req.Context(), w, r.d, ToolAddOntology, toolArgs, "result")
}

func (r *Router) handleAddOntologyRelation(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	sourceID, _ := args["source_id"].(string)
	targetID, _ := args["target_id"].(string)
	relation, _ := args["relation"].(string)
	if sourceID == "" || targetID == "" || relation == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "source_id, target_id and relation are required", nil)
		return
	}
	dispatch(req.Context(), w, r.d, ToolAddOntology, map[string]any{
		"source_id": sourceID,
		"target_id": targetID,
		"relation":  relation,
	}, "result")
}

var learnTypes = map[string]bool{
	"gotcha": true, "correction": true, "convention": true, "error-fix": true, "api-drift": true,
}

func (r *Router) handleLearn(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	learnType, _ := args["type"].(string)
	if !learnTypes[learnType] {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "type must be one of gotcha, correction, convention, error-fix, api-drift", map[string]any{"field": "type", "got": learnType})
		return
	}
	content, _ := args["content"].(string)
	if content == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "content is required", map[string]any{"field": "content"})
		return
	}
	dispatch(req.Context(), w, r.d, ToolLearn, args, "result")
}

func (r *Router) handleCommit(w http.ResponseWriter, req *http.Request) {
	args, err := decodeOptionalJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	dispatch(req.Context(), w, r.d, ToolCommit, args, "result")
}

func (r *Router) handleCapture(w http.ResponseWriter, req *http.Request) {
	args, err := decodeJSONBody(req)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	content, _ := args["content"].(string)
	if content == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "content is required", map[string]any{"field": "content"})
		return
	}
	// Enforced at the edge with 413 (INT-07: a retried capture re-spends
	// LLM budget; the tool's own check stays as backstop).
	if len(content) > maxCaptureBytes {
		writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "content exceeds the 100 KB capture limit", map[string]any{"field": "content", "max_bytes": maxCaptureBytes})
		return
	}
	dispatch(req.Context(), w, r.d, ToolCapture, args, "result")
}
