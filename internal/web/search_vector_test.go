package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
)

// stubEmbedder returns a fixed vector for every input.
type stubEmbedder struct{ v []float32 }

func (s stubEmbedder) Embed(string) ([]float32, error) { return s.v, nil }
func (s stubEmbedder) Dimensions() int                 { return len(s.v) }
func (s stubEmbedder) Name() string                    { return "stub" }

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
