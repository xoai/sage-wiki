package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupTestProject(t *testing.T) *WebServer {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Create some test articles
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "self-attention.md"), []byte(`---
concept: self-attention
confidence: high
---

## Definition

Self-attention computes contextual representations.

## See also

- [[multi-head-attention]]
`), 0644)

	srv, err := NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	// Add test data to stores
	srv.mem.Add(memory.Entry{
		ID:          "concept:self-attention",
		Content:     "Self-attention computes contextual representations",
		Tags:        []string{"concept"},
		ArticlePath: "wiki/concepts/self-attention.md",
	})

	srv.ont.AddEntity(ontology.Entity{
		ID: "self-attention", Type: "concept", Name: "Self-Attention",
	})
	srv.ont.AddEntity(ontology.Entity{
		ID: "multi-head-attention", Type: "concept", Name: "Multi-Head Attention",
	})
	srv.ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "multi-head-attention", TargetID: "self-attention", Relation: "extends",
	})

	return srv
}

func TestHandleTree(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/tree", nil)
	w := httptest.NewRecorder()

	srv.handleTree(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var tree map[string]any
	json.Unmarshal(w.Body.Bytes(), &tree)

	concepts, ok := tree["concepts"].([]any)
	if !ok || len(concepts) == 0 {
		t.Error("expected concepts in tree")
	}
}

func TestHandleStatus(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()

	srv.handleStatus(w, req)

	var status map[string]any
	json.Unmarshal(w.Body.Bytes(), &status)

	if status["project"] != "test" {
		t.Errorf("expected project 'test', got %v", status["project"])
	}
	if status["entities"].(float64) != 2 {
		t.Errorf("expected 2 entities, got %v", status["entities"])
	}
}

func TestHandleArticle(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/articles/concepts/self-attention.md", nil)
	w := httptest.NewRecorder()

	srv.handleArticle(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var article map[string]any
	json.Unmarshal(w.Body.Bytes(), &article)

	if article["path"] != "concepts/self-attention.md" {
		t.Errorf("unexpected path: %v", article["path"])
	}
	body, _ := article["body"].(string)
	if body == "" {
		t.Error("expected article body")
	}
}

func TestHandleArticlePathTraversal(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/articles/../../etc/passwd", nil)
	w := httptest.NewRecorder()

	srv.handleArticle(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for path traversal, got %d", w.Code)
	}
}

func TestHandleSearch(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/search?q=attention", nil)
	w := httptest.NewRecorder()

	srv.handleSearch(w, req)

	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)

	results, ok := result["results"].([]any)
	if !ok || len(results) == 0 {
		t.Error("expected search results")
	}
}

func TestHandleGraph(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/graph", nil)
	w := httptest.NewRecorder()

	srv.handleGraph(w, req)

	var graph map[string]any
	json.Unmarshal(w.Body.Bytes(), &graph)

	nodes, ok := graph["nodes"].([]any)
	if !ok || len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}
	edges, ok := graph["edges"].([]any)
	if !ok || len(edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(edges))
	}
}

func TestHandleGraphNeighborhood(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/graph?center=self-attention&depth=1", nil)
	w := httptest.NewRecorder()

	srv.handleGraph(w, req)

	var graph map[string]any
	json.Unmarshal(w.Body.Bytes(), &graph)

	nodes, ok := graph["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		t.Error("expected nodes in neighborhood")
	}
}

// TestWebGraphCenterResolvesAlias: centering the graph on an ALIAS must land
// on the canonical's neighborhood — one resolution feeding the traversal, the
// node set, the center-entity lookup, AND the response's additive "center"
// field, which is how the frontend learns which node to highlight.
func TestWebGraphCenterResolvesAlias(t *testing.T) {
	srv := setupTestProject(t)
	if err := srv.ont.AddEntity(ontology.Entity{ID: "sa", Type: "concept", Name: "SA"}); err != nil {
		t.Fatalf("AddEntity: %v", err)
	}
	if _, err := srv.ont.LinkAlias(store.EntityAlias{
		Alias: "sa", CanonicalID: "self-attention", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatalf("LinkAlias: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/graph?center=sa&depth=1", nil)
	w := httptest.NewRecorder()
	srv.handleGraph(w, req)

	var graph struct {
		Center string `json:"center"`
		Nodes  []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		Edges []struct {
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if graph.Center != "self-attention" {
		t.Errorf("center field = %q, want the canonical %q", graph.Center, "self-attention")
	}
	hasCanon := false
	for _, n := range graph.Nodes {
		if n.ID == "self-attention" {
			hasCanon = true
		}
	}
	if !hasCanon {
		t.Errorf("canonical node missing from alias-centered graph: %+v", graph.Nodes)
	}
	hasEdge := false
	for _, e := range graph.Edges {
		// The harness wires multi-head-attention → self-attention; direction
		// is the fixture's business, presence is this test's.
		if (e.Source == "self-attention" && e.Target == "multi-head-attention") ||
			(e.Source == "multi-head-attention" && e.Target == "self-attention") {
			hasEdge = true
		}
	}
	if !hasEdge {
		t.Errorf("canonical's edge missing from alias-centered graph: %+v", graph.Edges)
	}
}

func TestHandleFile(t *testing.T) {
	srv := setupTestProject(t)

	// Create a test image
	conceptsDir := filepath.Join(srv.projectDir, srv.cfg.Output, "concepts")
	os.WriteFile(filepath.Join(conceptsDir, "test.png"), []byte("fakepng"), 0644)

	req := httptest.NewRequest("GET", "/api/files/concepts/test.png", nil)
	w := httptest.NewRecorder()
	srv.handleFile(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected image/png, got %s", ct)
	}
}

func TestHandleFileTraversal(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/files/../../etc/passwd", nil)
	w := httptest.NewRecorder()
	srv.handleFile(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleFileDisallowedType(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/files/concepts/test.exe", nil)
	w := httptest.NewRecorder()
	srv.handleFile(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for .exe, got %d", w.Code)
	}
}

// A file inside the project but OUTSIDE the output dir (raw/ sources, config,
// .manifest.json) must not be reachable via ../ from the file/article handlers.
// The old containment scoped to the project root served these; scoping to the
// output dir closes the over-exposure. (SEC-01, P0-2)
func TestHandleFileRawOverExposureBlocked(t *testing.T) {
	srv := setupTestProject(t)
	rawDir := filepath.Join(srv.projectDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(rawDir, "leak.png"), []byte("fakepng"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/../raw/leak.png", nil)
	w := httptest.NewRecorder()
	srv.handleFile(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for ../raw over-exposure, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleArticleRawOverExposureBlocked(t *testing.T) {
	srv := setupTestProject(t)
	rawDir := filepath.Join(srv.projectDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(rawDir, "secret.md"), []byte("---\n---\nsecret"), 0o644)

	req := httptest.NewRequest("GET", "/api/articles/../raw/secret.md", nil)
	w := httptest.NewRecorder()
	srv.handleArticle(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for ../raw over-exposure, got %d: %s", w.Code, w.Body.String())
	}
}

// A percent-encoded traversal (..%2f) must be rejected just like a literal ../.
// net/http decodes %2f→/ into r.URL.Path before the handler sees it, so the
// cleaned ../ form reaches pathsafe — this pins that the decoded encoded-slash
// does not slip past the containment check.
func TestHandleFileEncodedSlashTraversalBlocked(t *testing.T) {
	srv := setupTestProject(t)
	rawDir := filepath.Join(srv.projectDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(rawDir, "leak.png"), []byte("fakepng"), 0o644)

	req := httptest.NewRequest("GET", "/api/files/..%2fraw/leak.png", nil)
	w := httptest.NewRecorder()
	srv.handleFile(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for encoded-slash traversal, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleQueryMethodNotAllowed(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/query", nil)
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleQueryBadBody(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("POST", "/api/query", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestCSRFProtection(t *testing.T) {
	srv := setupTestProject(t)
	handler := srv.Handler()

	req := httptest.NewRequest("POST", "/api/query", nil)
	req.Header.Set("Origin", "https://evil.com")
	req.Host = "127.0.0.1:3333"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for CSRF, got %d", w.Code)
	}
}

// TestNewWebServer_ServesAppData: the adopted container shares state — an
// entry written via a separate app.Open on the same project is visible to
// the server's stores (construction parity, P1-8 T3).
func TestNewWebServer_ServesAppData(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	a, err := app.Open(dir)
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	if err := a.Mem.Add(memory.Entry{ID: "concept:parity", Content: "container parity content"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	a.Close()

	srv, err := NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	defer srv.Close()

	n, err := srv.mem.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("server mem count = %d, want 1 (written via the container)", n)
	}
}
