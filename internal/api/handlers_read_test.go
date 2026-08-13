package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// newTestRouter builds a real MCP server on a temp greenfield project and a
// Router dispatching to it — the same wiring cmd/sage-wiki uses, so tests
// exercise the real handler chain, not a mock.
func newTestRouter(t *testing.T, cfgRewrite func(string) string) (*Router, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("InitGreenfield: %v", err)
	}
	// Repo-local git identity: CI runners have no global git config, and
	// wiki_commit tests commit to the initialized repo (git.Commit needs
	// user.email/user.name or it dies with "Author identity unknown").
	for _, kv := range [][2]string{{"user.email", "test@sage-wiki.local"}, {"user.name", "sage-wiki test"}} {
		if out, err := exec.Command("git", "-C", dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v (%s)", kv[0], err, out)
		}
	}
	if cfgRewrite != nil {
		p := filepath.Join(dir, "config.yaml")
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(cfgRewrite(string(raw))), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatalf("mcp.NewServer: %v", err)
	}
	t.Cleanup(func() { mcpSrv.Close() })
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	r := New(mcpSrv, cfg, dir, nil)
	mux := http.NewServeMux()
	r.RegisterRoutes(mux)
	return r, mux, dir
}

func serve(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rd)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func bodyJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not a JSON object: %v (%q)", err, w.Body.String())
	}
	return m
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		return ""
	}
	e, ok := m["error"].(map[string]any)
	if !ok {
		return ""
	}
	code, _ := e["code"].(string)
	return code
}

func writeArticle(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, "wiki", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// --- happy paths -----------------------------------------------------------

func TestSearch_HappyPath_PreservesUncompiledSources(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)

	// Seed one uncompiled (tier<3) source entry matching the query, via a
	// second handle on the same DB file (mirrors internal/memory t8 test).
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	mem := memory.NewStore(db)
	if err := mem.Add(memory.Entry{ID: "src:att.md", Content: "unique zebra token"}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO compile_items (source_path, tier) VALUES ('att.md', 1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	w := serve(t, mux, "GET", "/v1/search?query=zebra", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if _, ok := body["results"]; !ok {
		t.Fatalf("no results key: %v", body)
	}
	if body["uncompiled_sources"] != float64(1) {
		t.Fatalf("uncompiled_sources = %v, want 1 (passthrough fidelity)", body["uncompiled_sources"])
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestStatus_Structured(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if body["project"] != "test" {
		t.Fatalf("project = %v, want structured StatusInfo, not prose: %v", body["project"], body)
	}
	if _, ok := body["source_count"]; !ok {
		t.Fatalf("missing source_count: %v", body)
	}
}

func TestReadArticle_HappyPath(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	writeArticle(t, dir, "concepts/self-attention.md", "# Self-Attention\n\nBody.\n")

	w := serve(t, mux, "GET", "/v1/articles/concepts/self-attention.md", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if !strings.Contains(body["content"].(string), "Self-Attention") {
		t.Fatalf("content = %v", body["content"])
	}
	if body["path"] != "concepts/self-attention.md" {
		t.Fatalf("path = %v", body["path"])
	}
}

func TestTraverse_HappyPath(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()
	// Seed via the tool surface itself.
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "a", "entity_type": "concept", "entity_name": "A"})
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "b", "entity_type": "concept", "entity_name": "B"})
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"source_id": "a", "target_id": "b", "relation": "extends"})

	w := serve(t, mux, "GET", "/v1/ontology/a/traverse?depth=2&direction=outbound", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "b") {
		t.Fatalf("traverse result missing related entity: %s", w.Body.String())
	}
}

func TestEntities_HappyPath(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "a", "entity_type": "concept", "entity_name": "A"})

	w := serve(t, mux, "GET", "/v1/entities?type=concept", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"a"`) {
		t.Fatalf("entities = %s", w.Body.String())
	}
}

func TestProvenance_HappyPath(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/provenance?article=anything", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if body["article"] != "anything" {
		t.Fatalf("provenance = %v", body)
	}
}

func TestCompileDiff_HappyPath(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/compile/diff", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if _, ok := body["diff"]; !ok {
		t.Fatalf("missing diff envelope: %v", body)
	}
}

func TestGraphQuery_HappyPath(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"answer":"grounded answer","cited":[1]}`,
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer fake.Close()

	_, mux, dir := newTestRouter(t, func(raw string) string {
		return `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + fake.URL + `
models:
  summarize: gpt-4o-mini
  query: gpt-4o-mini
  write: gpt-4o-mini
compiler:
  auto_commit: false
`
	})
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "a", "entity_type": "concept", "entity_name": "A"})
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "b", "entity_type": "concept", "entity_name": "B"})
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"source_id": "a", "target_id": "b", "relation": "extends"})

	// GraphQA seeds retrieval from the memory store; add an entry the
	// question matches (mirrors internal/mcp TestMCPGraphQuery fixture).
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := memory.NewStore(db).Add(memory.Entry{ID: "concept:a", Content: "alpha chain zebra"}); err != nil {
		t.Fatal(err)
	}

	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"alpha chain zebra","hops":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "grounded answer") {
		t.Fatalf("graph answer = %s", w.Body.String())
	}
}

// --- validation matrix -----------------------------------------------------

func TestTraverse_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	cases := []struct {
		target string
		code   int
		err    string
	}{
		{"/v1/ontology/a/traverse?depth=9", 400, "invalid_argument"},
		{"/v1/ontology/a/traverse?depth=0", 400, "invalid_argument"},
		{"/v1/ontology/a/traverse?depth=x", 400, "invalid_argument"},
		{"/v1/ontology/a/traverse?direction=sideways", 400, "invalid_argument"},
	}
	for _, tc := range cases {
		w := serve(t, mux, "GET", tc.target, "")
		if w.Code != tc.code || errCode(t, w) != tc.err {
			t.Errorf("%s → %d %s, want %d %s", tc.target, w.Code, errCode(t, w), tc.code, tc.err)
		}
	}
}

func TestSearch_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/search", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("missing query → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/search?query=x&channels=bogus", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("bad channel → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/search?query=x&limit=-1", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("bad limit → %d %s", w.Code, errCode(t, w))
	}
}

func TestEntities_BadType(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/entities?type=widget", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Fatalf("type=widget → %d %s, want 400 invalid_argument", w.Code, errCode(t, w))
	}
}

func TestProvenance_ExactlyOneOf(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/provenance", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("neither → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/provenance?source=a&article=b", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("both → %d %s", w.Code, errCode(t, w))
	}
}

func TestReadArticle_TraversalAndMissAndDir(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0755); err != nil {
		t.Fatal(err)
	}

	// Literal .. never reaches the handler: ServeMux cleans the path and
	// redirects away from /v1 — assert no content leaks. The encoded form
	// (%2e%2e) survives cleaning, decodes to .. inside the wildcard, and
	// must hit the containment guard → 403.
	w := serve(t, mux, "GET", "/v1/articles/../../etc/passwd", "")
	if w.Code == 200 {
		t.Errorf("literal traversal returned 200 with body %q", w.Body.String())
	}
	w = serve(t, mux, "GET", "/v1/articles/%2e%2e/%2e%2e/etc/passwd", "")
	if w.Code != 403 || errCode(t, w) != "forbidden" {
		t.Errorf("encoded traversal → %d %s, want 403 forbidden", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/articles/concepts/does-not-exist.md", "")
	if w.Code != 404 || errCode(t, w) != "not_found" {
		t.Errorf("missing → %d %s, want 404 not_found", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/articles/concepts", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("directory → %d %s, want 400 invalid_argument", w.Code, errCode(t, w))
	}
}

func TestGraphQuery_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	cases := []struct {
		name string
		body string
		code int
		err  string
	}{
		{"missing question", `{"hops":2}`, 400, "invalid_argument"},
		{"hops zero", `{"question":"q","hops":0}`, 400, "invalid_argument"},
		{"hops six", `{"question":"q","hops":6}`, 400, "invalid_argument"},
		{"max_edges zero", `{"question":"q","max_edges":0}`, 400, "invalid_argument"},
		{"max_edges big", `{"question":"q","max_edges":501}`, 400, "invalid_argument"},
		{"bad as_of", `{"question":"q","as_of":"yesterday"}`, 400, "invalid_argument"},
		{"bad mode", `{"question":"q","mode":"sideways"}`, 400, "invalid_argument"},
		{"as_of with global", `{"question":"q","mode":"global","as_of":"2026-01-15T00:00:00Z"}`, 400, "invalid_argument"},
		{"empty body", ``, 400, "invalid_argument"},
		{"non-JSON body", `not json`, 400, "invalid_argument"},
	}
	for _, tc := range cases {
		w := serve(t, mux, "POST", "/v1/graph/query", tc.body)
		if w.Code != tc.code || errCode(t, w) != tc.err {
			t.Errorf("%s → %d %s, want %d %s", tc.name, w.Code, errCode(t, w), tc.code, tc.err)
		}
	}
}

// --- feature gates (412) ----------------------------------------------------

func TestGraphQuery_AsOf_TemporalDisabled(t *testing.T) {
	r, mux, _ := newTestRouter(t, nil)
	off := false
	r.cfg.Ontology.Temporal.Enabled = &off

	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"q","as_of":"2026-01-15T00:00:00Z"}`)
	if w.Code != 412 || errCode(t, w) != "feature_disabled" {
		t.Fatalf("as_of with temporal disabled → %d %s, want 412 feature_disabled", w.Code, errCode(t, w))
	}
}

func TestGraphQuery_AsOf_TemporalEnabledControl(t *testing.T) {
	// Temporal defaults to true (EnabledOrDefault). The pre-check must NOT
	// fire; the request proceeds into the tool, which fails later (no LLM
	// configured) — anything but 400/412 proves the gate passed.
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"q","as_of":"2026-01-15T00:00:00Z"}`)
	if w.Code == 412 || w.Code == 400 {
		t.Fatalf("temporal-enabled control → %d (%s); pre-check should not fire", w.Code, w.Body.String())
	}
}

func TestGraphQuery_Global_CommunitiesDisabled(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"q","mode":"global"}`)
	if w.Code != 412 || errCode(t, w) != "feature_disabled" {
		t.Fatalf("mode=global with communities disabled → %d %s, want 412 feature_disabled", w.Code, errCode(t, w))
	}
}

func TestGraphQuery_Global_CommunitiesEnabledControl(t *testing.T) {
	// Pre-check passes (router cfg enabled); the tool's own cfg still has
	// communities disabled, so the tool's gate produces an unclassified
	// error → 500, proving the REST 412 did NOT fire.
	r, mux, _ := newTestRouter(t, nil)
	r.cfg.Ontology.Communities.Enabled = true
	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"q","mode":"global"}`)
	if w.Code == 412 {
		t.Fatalf("communities-enabled control → 412; pre-check should not fire (%s)", w.Body.String())
	}
	if w.Code != 500 || !strings.Contains(w.Body.String(), "communities") {
		t.Fatalf("control → %d (%s), want the tool's own gate as 500", w.Code, w.Body.String())
	}
}
