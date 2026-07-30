package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
)

// The 18 MCP tool names (04 §mapping). Additive-only: never rename.
const (
	ToolSearch        = "wiki_search"
	ToolRead          = "wiki_read"
	ToolStatus        = "wiki_status"
	ToolOntologyQuery = "wiki_ontology_query"
	ToolGraphQuery    = "wiki_graph_query"
	ToolList          = "wiki_list"
	ToolProvenance    = "wiki_provenance"
	ToolAddSource     = "wiki_add_source"
	ToolWriteSummary  = "wiki_write_summary"
	ToolWriteArticle  = "wiki_write_article"
	ToolAddOntology   = "wiki_add_ontology"
	ToolLearn         = "wiki_learn"
	ToolCommit        = "wiki_commit"
	ToolCompileDiff   = "wiki_compile_diff"
	ToolCapture       = "wiki_capture"
	ToolCompileTopic  = "wiki_compile_topic"
	ToolCompile       = "wiki_compile"
	ToolLint          = "wiki_lint"
)

// Dispatcher is the seam to the MCP server: every /v1 route reaches its
// capability through the same function the tool registers, never a copy.
// *mcp.Server satisfies it via its exported CallTool (internal/mcp).
type Dispatcher interface {
	CallTool(ctx context.Context, name string, req mcp.CallToolRequest) *mcp.CallToolResult
}

// pathSensitiveTools embed filesystem paths in their error text. Their
// IsError messages must not be returned to clients (04 §Error model: 500
// messages must not leak paths); the tool text goes to the server log.
var pathSensitiveTools = map[string]bool{
	ToolRead:      true,
	ToolAddSource: true,
}

// toolRequest builds the CallToolRequest for a dispatch.
func toolRequest(tool string, args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: tool, Arguments: args},
	}
}

// dispatch invokes a tool and translates the result to the HTTP response.
//
// Translation rules (spec §Rationale): a tool result whose text parses as a
// JSON object or array is re-emitted verbatim (preserving every field, e.g.
// uncompiled_sources); any other text is wrapped as {envelopeKey: text}.
// envelopeKey == "" selects JSON-passthrough with a "result" fallback.
// IsError results map to 500 internal — edge validation produces precise
// codes before dispatch, so anything reaching here is unclassified. Error
// text is never string-matched to pick a code.
func dispatch(ctx context.Context, w http.ResponseWriter, d Dispatcher, tool string, args map[string]any, envelopeKey string) {
	res := d.CallTool(ctx, tool, toolRequest(tool, args))
	if res == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "tool returned no result", nil)
		return
	}
	text := resultText(res)
	if res.IsError {
		if pathSensitiveTools[tool] {
			log.Printf("api: %s failed: %s", tool, text)
			writeError(w, http.StatusInternalServerError, CodeInternal, fmt.Sprintf("%s failed", tool), nil)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, text, nil)
		return
	}
	if envelopeKey == "" {
		var parsed any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			switch parsed.(type) {
			case map[string]any, []any:
				writeJSON(w, http.StatusOK, parsed)
				return
			}
		}
		envelopeKey = "result"
	}
	writeJSON(w, http.StatusOK, map[string]any{envelopeKey: text})
}

// resultText concatenates the text contents of a tool result.
func resultText(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

// decodeJSONBody decodes a request body into a tool argument map. Numbers
// decode as float64, matching what MCP handlers assert (req.GetArguments().
// (float64)). Empty or malformed bodies are caller-visible errors — the
// handlers translate them to 400 invalid_argument, never 500.
func decodeJSONBody(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("request body is required")
	}
	var args map[string]any
	if err := json.Unmarshal(body, &args); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	return args, nil
}
