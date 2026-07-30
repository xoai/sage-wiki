package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

type spyDispatcher struct {
	calls    int
	lastName string
	lastArgs map[string]any
	result   *mcp.CallToolResult
}

func (s *spyDispatcher) CallTool(_ context.Context, name string, req mcp.CallToolRequest) *mcp.CallToolResult {
	s.calls++
	s.lastName = name
	s.lastArgs = req.GetArguments()
	return s.result
}

func textRes(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// call invokes a tool on a real MCP server and fails the test on tool error.
func call(t *testing.T, srv interface {
	CallTool(context.Context, string, mcp.CallToolRequest) *mcp.CallToolResult
}, name string, args map[string]any) {
	t.Helper()
	res := srv.CallTool(context.Background(), name, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if res.IsError {
		t.Fatalf("%s: %s", name, resultText(res))
	}
}

func errRes(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: msg}},
	}
}

func TestWriteError_EnvelopeShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, CodeInvalidArgument, "depth must be between 1 and 5", map[string]any{"field": "depth", "got": 9})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the envelope: %v (%q)", err, w.Body.String())
	}
	e := body["error"]
	if e["code"] != "invalid_argument" || e["message"] != "depth must be between 1 and 5" {
		t.Fatalf("envelope = %v", e)
	}
	details, ok := e["details"].(map[string]any)
	if !ok || details["field"] != "depth" {
		t.Fatalf("details = %v", e["details"])
	}
}

func TestWriteError_OmitsDetailsWhenNil(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusInternalServerError, CodeInternal, "boom", nil)
	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["error"]["details"]; present {
		t.Fatalf("details should be omitted, got %v", body["error"])
	}
}

func TestErrorCodeVocabulary(t *testing.T) {
	codes := map[string]string{
		"CodeInvalidArgument": CodeInvalidArgument,
		"CodeUnauthenticated": CodeUnauthenticated,
		"CodeForbidden":       CodeForbidden,
		"CodeNotFound":        CodeNotFound,
		"CodeConflict":        CodeConflict,
		"CodeFeatureDisabled": CodeFeatureDisabled,
		"CodePayloadTooLarge": CodePayloadTooLarge,
		"CodeRateLimited":     CodeRateLimited,
		"CodeInternal":        CodeInternal,
		"CodeUnavailable":     CodeUnavailable,
	}
	want := map[string]string{
		"CodeInvalidArgument": "invalid_argument",
		"CodeUnauthenticated": "unauthenticated",
		"CodeForbidden":       "forbidden",
		"CodeNotFound":        "not_found",
		"CodeConflict":        "conflict",
		"CodeFeatureDisabled": "feature_disabled",
		"CodePayloadTooLarge": "payload_too_large",
		"CodeRateLimited":     "rate_limited",
		"CodeInternal":        "internal",
		"CodeUnavailable":     "unavailable",
	}
	for name, got := range codes {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}

func TestDispatch_JSONPassthrough(t *testing.T) {
	spy := &spyDispatcher{result: textRes(`{"results":[],"uncompiled_sources":["a.md"]}`)}
	w := httptest.NewRecorder()
	dispatch(context.Background(), w, spy, ToolSearch, map[string]any{"query": "x", "limit": float64(3)}, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	uc, ok := body["uncompiled_sources"].([]any)
	if !ok || len(uc) != 1 || uc[0] != "a.md" {
		t.Fatalf("uncompiled_sources not preserved: %v", body)
	}
	if spy.lastArgs["limit"] != float64(3) {
		t.Fatalf("limit arg = %#v, want float64(3)", spy.lastArgs["limit"])
	}
}

func TestDispatch_ProseEnvelope(t *testing.T) {
	spy := &spyDispatcher{result: textRes("Learning stored: [gotcha] x")}
	w := httptest.NewRecorder()
	dispatch(context.Background(), w, spy, ToolLearn, map[string]any{"type": "gotcha", "content": "x"}, "result")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["result"] != "Learning stored: [gotcha] x" {
		t.Fatalf("body = %v", body)
	}
}

func TestDispatch_IsError500WithToolMessage(t *testing.T) {
	spy := &spyDispatcher{result: errRes("store unavailable: locked")}
	w := httptest.NewRecorder()
	dispatch(context.Background(), w, spy, ToolList, nil, "")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"]["code"] != "internal" || body["error"]["message"] != "store unavailable: locked" {
		t.Fatalf("body = %v", body)
	}
}

func TestDispatch_IsErrorPathTool_DoesNotLeakPaths(t *testing.T) {
	for _, tool := range []string{ToolRead, ToolAddSource} {
		spy := &spyDispatcher{result: errRes("failed to read /home/user/secret/wiki/out/x.md: permission denied")}
		w := httptest.NewRecorder()
		dispatch(context.Background(), w, spy, tool, nil, "")
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d", tool, w.Code)
		}
		if strings.Contains(w.Body.String(), "/home/user") {
			t.Fatalf("%s: response leaks filesystem path: %s", tool, w.Body.String())
		}
		var body map[string]map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"]["code"] != "internal" || body["error"]["message"] == "" {
			t.Fatalf("%s: body = %v", tool, body)
		}
	}
}

func TestDecodeJSONBody_EdgeCases(t *testing.T) {
	t.Run("empty body is invalid_argument", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(""))
		_, err := decodeJSONBody(req)
		if err == nil {
			t.Fatal("want error for empty body")
		}
	})
	t.Run("non-JSON body is invalid_argument", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader("not json"))
		_, err := decodeJSONBody(req)
		if err == nil {
			t.Fatal("want error for non-JSON body")
		}
	})
	t.Run("valid body decodes numbers as float64", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/graph/query", strings.NewReader(`{"question":"q","hops":2}`))
		args, err := decodeJSONBody(req)
		if err != nil {
			t.Fatal(err)
		}
		if args["hops"] != float64(2) {
			t.Fatalf("hops = %#v, want float64(2)", args["hops"])
		}
	})
}

func TestToolNameConstants(t *testing.T) {
	names := []string{
		ToolSearch, ToolRead, ToolStatus, ToolOntologyQuery, ToolGraphQuery,
		ToolList, ToolProvenance, ToolAddSource, ToolWriteSummary,
		ToolWriteArticle, ToolAddOntology, ToolLearn, ToolCommit,
		ToolCompileDiff, ToolCapture, ToolCompileTopic, ToolCompile, ToolLint,
	}
	want := []string{
		"wiki_search", "wiki_read", "wiki_status", "wiki_ontology_query",
		"wiki_graph_query", "wiki_list", "wiki_provenance", "wiki_add_source",
		"wiki_write_summary", "wiki_write_article", "wiki_add_ontology",
		"wiki_learn", "wiki_commit", "wiki_compile_diff", "wiki_capture",
		"wiki_compile_topic", "wiki_compile", "wiki_lint",
	}
	if len(names) != 18 {
		t.Fatalf("tool constant count = %d, want 18", len(names))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}
