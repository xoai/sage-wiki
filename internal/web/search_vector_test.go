package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// stubEmbedder returns a fixed vector for every input.
type stubEmbedder struct{ v []float32 }

func (s stubEmbedder) Embed(string) ([]float32, error) { return s.v, nil }
func (s stubEmbedder) Dimensions() int                 { return len(s.v) }
func (s stubEmbedder) Name() string                    { return "stub" }

// V-M1b (constructor wiring): NewWebServer must wire the project embedder —
// deleting the `embedder: a.Embedder()` assignment silently reverts web
// search to BM25-only with every handler test still green.
func TestNewWebServerWiresEmbedder(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// Pin a deterministic embed provider (constructed without dialing).
	edited := strings.Replace(string(raw),
		"provider: auto",
		"provider: openai\n  model: text-embedding-3-small\n  api_key: test-key", 1)
	if edited == string(raw) {
		t.Fatal("fixture drift: embed provider line not found in greenfield config")
	}
	if err := os.WriteFile(cfgPath, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	srv, err := NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	if srv.embedder == nil {
		t.Error("NewWebServer left embedder nil with an embed-configured project — web search would silently run BM25-only")
	}
}

// V-M1b: a result findable only by vector similarity must appear — the
// handler previously passed a nil query vector, making web search BM25-only.
func TestHandleSearchUsesQueryVector(t *testing.T) {
	srv := setupTestProject(t)

	// No lexical overlap with the query "sly fox" (porter-stemmed FTS
	// cannot match); only the vector leg can surface this entry.
	srv.mem.Add(memory.Entry{
		ID:          "concept:vulpine",
		Content:     "Vulpes vulpes exhibits remarkable adaptability",
		Tags:        []string{"concept"},
		ArticlePath: "wiki/concepts/vulpine.md",
	})
	vec := []float32{1, 0, 0}
	if err := srv.vec.Upsert("concept:vulpine", vec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	srv.embedder = stubEmbedder{v: vec}

	req := httptest.NewRequest("GET", "/api/search?q=sly+fox", nil)
	w := httptest.NewRecorder()
	srv.handleSearch(w, req)

	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	results, _ := result["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected a vector-only hit; got none — query vector not passed to hybrid search")
	}
	hit, _ := results[0].(map[string]any)
	if hit["id"] != "concept:vulpine" {
		t.Errorf("expected concept:vulpine, got %v", hit["id"])
	}
}
