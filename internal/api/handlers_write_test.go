package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
)

func TestAddSource_HappyPath(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "raw"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "note.md"), []byte("# Note\n"), 0644); err != nil {
		t.Fatal(err)
	}

	w := serve(t, mux, "POST", "/v1/sources", `{"path":"raw/note.md"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	body := bodyJSON(t, w)
	if !strings.Contains(body["result"].(string), "Source added") {
		t.Fatalf("result = %v", body["result"])
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestAddSource_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	cases := []struct {
		name string
		body string
		code int
		err  string
	}{
		{"missing path", `{"type":"article"}`, 400, "invalid_argument"},
		{"bad type", `{"path":"raw/x.md","type":"video"}`, 400, "invalid_argument"},
		{"empty body", ``, 400, "invalid_argument"},
		{"non-JSON body", `not json`, 400, "invalid_argument"},
	}
	for _, tc := range cases {
		w := serve(t, mux, "POST", "/v1/sources", tc.body)
		if w.Code != tc.code || errCode(t, w) != tc.err {
			t.Errorf("%s → %d %s, want %d %s", tc.name, w.Code, errCode(t, w), tc.code, tc.err)
		}
	}
}

func TestAddSource_Traversal(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/sources", `{"path":"../../etc/passwd"}`)
	if w.Code != 403 || errCode(t, w) != "forbidden" {
		t.Fatalf("traversal → %d %s, want 403 forbidden", w.Code, errCode(t, w))
	}
}

func TestWriteSummary_HappyPath(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "PUT", "/v1/summaries", `{"source":"raw/note.md","content":"A summary.","concepts":"alpha,beta"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Summary written") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWriteSummary_MissingFields(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "PUT", "/v1/summaries", `{"source":"raw/note.md"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Fatalf("missing content → %d %s", w.Code, errCode(t, w))
	}
}

func TestWriteArticle_HappyPath(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "PUT", "/v1/articles/self-attention", `{"content":"---\nconcept: self-attention\n---\n\n## Definition\n\nX.\n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Article written") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWriteArticle_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "PUT", "/v1/articles/Bad_Caps", `{"content":"x"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("bad concept shape → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "PUT", "/v1/articles/ok-concept", `{}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("missing content → %d %s", w.Code, errCode(t, w))
	}
}

func TestOntologySplit_ReachesSameHandler(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)

	w := serve(t, mux, "POST", "/v1/ontology/entities", `{"id":"x","type":"concept","name":"X"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Entity created: x") {
		t.Fatalf("entities → %d (%s)", w.Code, w.Body.String())
	}
	w = serve(t, mux, "POST", "/v1/ontology/entities", `{"id":"y","type":"concept","name":"Y"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("entities y → %d (%s)", w.Code, w.Body.String())
	}
	w = serve(t, mux, "POST", "/v1/ontology/relations", `{"source_id":"x","target_id":"y","relation":"extends"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "extends") {
		t.Fatalf("relations → %d (%s)", w.Code, w.Body.String())
	}

	// The MCP tool still accepts the combined form (additive-only rule).
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"entity_id": "z", "entity_type": "concept", "entity_name": "Z"})
	call(t, mcpSrv, "wiki_add_ontology", map[string]any{"source_id": "z", "target_id": "x", "relation": "extends"})
}

func TestOntologySplit_Validation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/ontology/entities", `{"type":"concept"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("entities missing id → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "POST", "/v1/ontology/relations", `{"source_id":"x","relation":"extends"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("relations missing target_id → %d %s", w.Code, errCode(t, w))
	}
}

func TestLearn_HappyPathAndValidation(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/learnings", `{"type":"gotcha","content":"a gotcha","tags":"go,api"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Learning stored") {
		t.Fatalf("learn → %d (%s)", w.Code, w.Body.String())
	}
	w = serve(t, mux, "POST", "/v1/learnings", `{"type":"opinion","content":"x"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("bad type → %d %s", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "POST", "/v1/learnings", `{"type":"gotcha"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("missing content → %d %s", w.Code, errCode(t, w))
	}
}

func TestGitCommit_HappyPath(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/git/commit", `{"message":"test commit"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Committed") {
		t.Fatalf("commit → %d (%s)", w.Code, w.Body.String())
	}
	// message is optional
	w = serve(t, mux, "POST", "/v1/git/commit", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("commit without message → %d (%s)", w.Code, w.Body.String())
	}
}

func TestCapture_PayloadCap(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	big := strings.Repeat("x", 100*1024+1)
	body, _ := json.Marshal(map[string]string{"content": big})
	w := serve(t, mux, "POST", "/v1/capture", string(body))
	if w.Code != 413 || errCode(t, w) != "payload_too_large" {
		t.Fatalf("100KiB+1 → %d %s, want 413 payload_too_large", w.Code, errCode(t, w))
	}
}

func TestCapture_HappyPath_NoLLMSavesRaw(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/capture", `{"content":"small capture"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("capture → %d (%s)", w.Code, w.Body.String())
	}
}

func TestCapture_MissingContent(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/capture", `{"context":"nothing"}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Fatalf("missing content → %d %s", w.Code, errCode(t, w))
	}
}
