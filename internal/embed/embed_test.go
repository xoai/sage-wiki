package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestCascadeTier1(t *testing.T) {
	e := NewCascade("openai", "sk-test", "")
	if e == nil {
		t.Fatal("expected embedder for openai")
	}
	if e.Name() != "openai/text-embedding-3-small" {
		t.Errorf("unexpected name: %s", e.Name())
	}
	if e.Dimensions() != 1536 {
		t.Errorf("expected 1536 dims, got %d", e.Dimensions())
	}
}

func TestCascadeAnthropicFallsThrough(t *testing.T) {
	// Anthropic has no embedding API — should fall through
	e := NewCascade("anthropic", "sk-ant-test", "")
	// This will return nil unless Ollama is running
	// We can't control Ollama in tests, so just verify no panic
	_ = e
}

func TestCascadeNoProvider(t *testing.T) {
	e := NewCascade("", "", "")
	// Should return nil (no provider, no Ollama assumed)
	// Can't guarantee nil because Ollama might be running locally
	_ = e
}

func TestAPIEmbedderWithMockServer(t *testing.T) {
	// Mock OpenAI embedding API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.Error(w, "not found", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "unauthorized", 401)
			return
		}

		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer server.Close()

	e := &APIEmbedder{
		provider: "openai",
		model:    "text-embedding-3-small",
		apiKey:   "sk-test",
		baseURL:  server.URL,
		dims:     3,
	}

	vec, err := e.Embed("test text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3 dimensions, got %d", len(vec))
	}
	if vec[0] != 0.1 {
		t.Errorf("expected 0.1, got %f", vec[0])
	}
}

func TestAPIEmbedderErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", 429)
	}))
	defer server.Close()

	e := &APIEmbedder{
		provider: "openai",
		model:    "text-embedding-3-small",
		apiKey:   "sk-test",
		baseURL:  server.URL,
		dims:     1536,
	}

	_, err := e.Embed("test")
	if err == nil {
		t.Error("expected error on 429")
	}
}

func TestDefaultModels(t *testing.T) {
	providers := []string{"openai", "gemini", "voyage", "mistral"}
	for _, p := range providers {
		model, ok := defaultModels[p]
		if !ok {
			t.Errorf("missing default model for %s", p)
			continue
		}
		dims, ok := defaultDimensions[model]
		if !ok {
			t.Errorf("missing default dimensions for %s", model)
			continue
		}
		if dims <= 0 {
			t.Errorf("invalid dimensions %d for %s", dims, model)
		}
	}
}

func TestResolveOllamaBaseURL(t *testing.T) {
	if got := ResolveOllamaBaseURL("ollama", "http://example:11434/"); got != "http://example:11434" {
		t.Fatalf("expected trimmed ollama URL, got %q", got)
	}

	if got := ResolveOllamaBaseURL("openai-compatible", "http://example:11434"); got != defaultOllamaBaseURL {
		t.Fatalf("expected localhost fallback for non-ollama provider, got %q", got)
	}
}

func TestOllamaEmbedderUsesConfiguredBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "nomic-embed-text:latest"}},
			})
		case "/api/embeddings":
			json.NewEncoder(w).Encode(map[string]any{
				"embedding": []float32{0.1, 0.2, 0.3},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	e := NewCascade("ollama", "", server.URL)
	if e == nil {
		t.Fatal("expected embedder for remote ollama")
	}

	ollama, ok := e.(*OllamaEmbedder)
	if !ok {
		t.Fatalf("expected OllamaEmbedder, got %T", e)
	}
	if ollama.baseURL != server.URL {
		t.Fatalf("expected base URL %q, got %q", server.URL, ollama.baseURL)
	}

	vec, err := ollama.Embed("test text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 dimensions, got %d", len(vec))
	}
}

func TestNewForConfigAutoKeepsCurrentBehavior(t *testing.T) {
	cfg := &config.Config{
		API: config.APIConfig{
			Provider: "openai",
			APIKey:   "sk-test",
		},
		Embed: &config.EmbedConfig{
			Provider: "auto",
		},
	}

	e := NewForConfig(cfg)
	if e == nil {
		t.Fatal("expected embedder")
	}
	if e.Name() != "openai/text-embedding-3-small" {
		t.Fatalf("unexpected embedder name %q", e.Name())
	}
}

func TestNewForConfigUsesExplicitEmbedModel(t *testing.T) {
	cfg := &config.Config{
		API: config.APIConfig{
			Provider: "openai",
			APIKey:   "sk-test",
		},
		Embed: &config.EmbedConfig{
			Provider:   "openai",
			Model:      "text-embedding-3-small",
			Dimensions: 1536,
		},
	}

	e := NewForConfig(cfg)
	if e == nil {
		t.Fatal("expected embedder")
	}

	api, ok := e.(*APIEmbedder)
	if !ok {
		t.Fatalf("expected APIEmbedder, got %T", e)
	}
	if api.model != "text-embedding-3-small" {
		t.Fatalf("unexpected model %q", api.model)
	}
	if api.dims != 1536 {
		t.Fatalf("unexpected dimensions %d", api.dims)
	}
}
