package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// graphQueryProject builds a project whose config routes LLM calls to the
// fake server (the TestCapture_UntrustedDelimiter mechanism).
func graphQueryProject(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")
	cfg := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + baseURL + `
models:
  summarize: gpt-4o-mini
  query: gpt-4o-mini
compiler:
  auto_commit: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestMCPGraphQuery: end-to-end through CallTool dispatch — seed search,
// serialization, the LLM round-trip, and the structured response. The
// explicit hops:1 arg is the arg-wiring half of the hop dimension: the
// captured prompt must carry the hop-1 edge and not the hop-2 one.
func TestMCPGraphQuery(t *testing.T) {
	var captured string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for _, mm := range body["messages"].([]any) {
			m := mm.(map[string]any)
			if m["role"] == "user" {
				captured, _ = m["content"].(string)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"answer":"grounded answer","cited":[1]}`,
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer fake.Close()

	dir := graphQueryProject(t, fake.URL)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	for _, e := range []ontology.Entity{
		{ID: "A", Type: "concept", Name: "A"},
		{ID: "B", Type: "concept", Name: "B"},
		{ID: "C", Type: "concept", Name: "C"},
	} {
		if err := srv.ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.ont.AddRelation(ontology.Relation{ID: "r1", SourceID: "A", TargetID: "B",
		Relation: "extends", SourceDoc: "raw/a.md", Confidence: 0.9}); err != nil {
		t.Fatal(err)
	}
	if err := srv.ont.AddRelation(ontology.Relation{ID: "r2", SourceID: "B", TargetID: "C",
		Relation: "extends"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.mem.Add(memory.Entry{ID: "concept:A", Content: "alpha chain zebra"}); err != nil {
		t.Fatal(err)
	}

	result := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "alpha chain zebra",
		"hops":     float64(1),
	}))
	if result.IsError {
		t.Fatalf("wiki_graph_query error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	var resp struct {
		Answer string `json:"answer"`
		Cited  []struct {
			Line      string `json:"line"`
			SourceDoc string `json:"source_doc"`
		} `json:"cited"`
		Truncated bool     `json:"truncated"`
		Seeds     []string `json:"seeds"`
	}
	text := result.Content[0].(mcplib.TextContent).Text
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("response is not the structured object: %v\n%s", err, text)
	}
	if resp.Answer != "grounded answer" {
		t.Errorf("answer = %q", resp.Answer)
	}
	if len(resp.Cited) != 1 || resp.Cited[0].SourceDoc != "raw/a.md" {
		t.Errorf("cited = %+v, want the one provenance-bearing edge", resp.Cited)
	}
	if len(resp.Seeds) != 1 || resp.Seeds[0] != "A" {
		t.Errorf("seeds = %v, want [A]", resp.Seeds)
	}

	if captured == "" {
		t.Fatal("no LLM request captured — assertions were vacuous")
	}
	if !strings.Contains(captured, "(A) --[extends]--> (B)") {
		t.Errorf("hop-1 edge missing from prompt:\n%s", captured)
	}
	if strings.Contains(captured, "(C)") {
		t.Errorf("hops:1 arg not honored — C reached:\n%s", captured)
	}
}

// TestMCPGraphQuerySchemasAdditive pins every tool's serialized SCHEMA
// per-name against a golden — a name-list pin would miss an edited schema on
// an existing tool, and ListTools returns a map, so per-name comparison is
// also what keeps the pin order-independent. ServerTool itself carries a
// handler func and will not marshal; the golden serializes the .Tool field.
//
// Regenerate deliberately: SAGE_UPDATE_MCP_SCHEMAS=1 go test -run
// TestMCPGraphQuerySchemasAdditive ./internal/mcp/
func TestMCPGraphQuerySchemasAdditive(t *testing.T) {
	dir := setupTestProject(t)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	tools := srv.MCPServer().ListTools()
	got := make(map[string]json.RawMessage, len(tools))
	for name, st := range tools {
		b, err := json.MarshalIndent(st.Tool, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		got[name] = b
	}

	goldenPath := filepath.Join("testdata", "mcp_tool_schemas.json")
	if os.Getenv("SAGE_UPDATE_MCP_SCHEMAS") == "1" {
		names := make([]string, 0, len(got))
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		ordered := make(map[string]json.RawMessage, len(got))
		for _, n := range names {
			ordered[n] = got[n]
		}
		b, _ := json.MarshalIndent(ordered, "", "  ")
		os.MkdirAll("testdata", 0755)
		if err := os.WriteFile(goldenPath, b, 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden rewritten: %s (%d tools)", goldenPath, len(got))
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden missing (%v) — regenerate with SAGE_UPDATE_MCP_SCHEMAS=1", err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	if _, ok := got["wiki_graph_query"]; !ok {
		t.Errorf("wiki_graph_query not registered")
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("tool %s vanished — schema change is not additive", name)
			continue
		}
		var a, b any
		json.Unmarshal(w, &a)
		json.Unmarshal(g, &b)
		ab, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if string(ab) != string(bb) {
			t.Errorf("tool %s schema drifted from golden — regenerate ONLY if the change is deliberate", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("tool %s not in golden — regenerate to record the addition", name)
		}
	}
}

// P3-6: as_of point-in-time graph QA — invalid values error before any LLM
// call; a valid value reaches the prompt with its disclosure note, and
// point-in-time subgraphs include edges that default (live-at-now) reads
// filter out.
func TestMCPGraphQueryAsOf(t *testing.T) {
	var captured string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for _, mm := range body["messages"].([]any) {
			m := mm.(map[string]any)
			if m["role"] == "user" {
				captured, _ = m["content"].(string)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"answer":"grounded answer","cited":[1]}`,
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer fake.Close()

	dir := graphQueryProject(t, fake.URL)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	for _, e := range []ontology.Entity{{ID: "A", Type: "concept", Name: "A"}, {ID: "B", Type: "concept", Name: "B"}} {
		if err := srv.ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	// Dead edge: valid 2020 → 2025. Default reads filter it; as_of 2022 sees it.
	if err := srv.ont.AddRelation(ontology.Relation{ID: "r-old", SourceID: "A", TargetID: "B",
		Relation: "extends", SourceDoc: "raw/a.md", Confidence: 0.9,
		ValidFrom: "2020-01-01T00:00:00Z", ValidTo: "2025-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.mem.Add(memory.Entry{ID: "concept:A", Content: "alpha chain zebra"}); err != nil {
		t.Fatal(err)
	}

	// Bad as_of errors before any LLM call.
	bad := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "alpha chain zebra", "as_of": "january 2022",
	}))
	if !bad.IsError {
		t.Error("invalid as_of must error")
	}
	if captured != "" {
		t.Error("invalid as_of must not reach the LLM")
	}

	// Default: dead edge filtered — the no-edges short-circuit answer.
	def := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "alpha chain zebra", "hops": float64(1),
	}))
	if def.IsError {
		t.Fatal("default call errored")
	}
	if !strings.Contains(def.Content[0].(mcplib.TextContent).Text, "no edges found") {
		t.Errorf("default must filter the dead edge, got: %s", def.Content[0].(mcplib.TextContent).Text)
	}

	// as_of 2022: edge visible, prompt discloses the window.
	ok := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "alpha chain zebra", "hops": float64(1), "as_of": "2022-06-01T00:00:00Z",
	}))
	if ok.IsError {
		t.Fatalf("as_of call errored: %s", ok.Content[0].(mcplib.TextContent).Text)
	}
	if !strings.Contains(captured, "(A) --[extends]--> (B)") {
		t.Errorf("as_of 2022 must include the then-valid edge:\n%s", captured)
	}
	if !strings.Contains(captured, "as of 2022-06-01T00:00:00Z") {
		t.Errorf("prompt must disclose the as-of window:\n%s", captured)
	}
}

// P3-5: wiki_graph_query mode=global — gated, invalid-mode error, and the
// enabled happy path over seeded communities.
func TestMCPGraphQueryGlobalMode(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": "A synthesized global answer citing [c0-0].",
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer fake.Close()

	dir := graphQueryProject(t, fake.URL)
	// Enable communities.
	cfgBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	os.WriteFile(filepath.Join(dir, "config.yaml"),
		append(cfgBytes, []byte("\nontology:\n  communities:\n    enabled: true\n")...), 0o644)

	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Invalid mode errors.
	bad := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "themes?", "mode": "sideways",
	}))
	if !bad.IsError {
		t.Error("invalid mode must error")
	}

	// No communities yet → ErrNoCommunities surfaced.
	empty := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "themes?", "mode": "global",
	}))
	if !empty.IsError || !strings.Contains(empty.Content[0].(mcplib.TextContent).Text, "no summarized communities") {
		t.Errorf("global with no communities must surface the sentinel, got: %v", empty.Content[0])
	}

	// Seed one summarized community (+ its index entry) and ask again.
	cs := srv.backend.Communities()
	for _, id := range []string{"a", "b", "c"} {
		if err := srv.ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cs.ReplaceDetection(
		[]store.Community{{ID: "c0-0", Level: 0, MemberCount: 3, UpdatedAt: "2026-07-29T00:00:00Z"}},
		map[string][]string{"c0-0": {"a", "b", "c"}}); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetSummary("c0-0", "attention optimization techniques", "h", "m"); err != nil {
		t.Fatal(err)
	}
	if err := srv.mem.Add(memory.Entry{ID: "community:c0-0", Content: "attention optimization techniques"}); err != nil {
		t.Fatal(err)
	}

	ok := srv.CallTool(context.Background(), "wiki_graph_query", makeToolRequest(map[string]any{
		"question": "attention optimization?", "mode": "global",
	}))
	if ok.IsError {
		t.Fatalf("global happy path errored: %s", ok.Content[0].(mcplib.TextContent).Text)
	}
	var resp struct {
		Answer string `json:"answer"`
		Cited  []struct {
			ID string `json:"id"`
		} `json:"cited"`
	}
	if err := json.Unmarshal([]byte(ok.Content[0].(mcplib.TextContent).Text), &resp); err != nil {
		t.Fatalf("response not structured: %v", err)
	}
	if resp.Answer == "" {
		t.Error("empty global answer")
	}
	if len(resp.Cited) != 1 || resp.Cited[0].ID != "c0-0" {
		t.Errorf("cited = %+v, want exactly [c0-0]", resp.Cited)
	}
}
