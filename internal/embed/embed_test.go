package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/llm"
)

func TestCascadeTier1(t *testing.T) {
	e := NewCascade("openai", "sk-test", "", nil)
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
	e := NewCascade("anthropic", "sk-ant-test", "", nil)
	// This will return nil unless Ollama is running
	// We can't control Ollama in tests, so just verify no panic
	_ = e
}

func TestCascadeNoProvider(t *testing.T) {
	e := NewCascade("", "", "", nil)
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

func TestCascadeTier0Override(t *testing.T) {
	// Tier 0 override should take priority over tier 1 auto-detection
	ov := &EmbedOverride{
		Provider:   "openai",
		Model:      "custom-embed-model",
		Dimensions: 1024,
		APIKey:     "sk-custom",
		BaseURL:    "https://custom.example.com/v1",
	}
	e := NewCascade("openai", "sk-default", "", ov)
	if e == nil {
		t.Fatal("expected embedder with override")
	}
	if e.Name() != "openai/custom-embed-model" {
		t.Errorf("expected override model, got: %s", e.Name())
	}
	if e.Dimensions() != 1024 {
		t.Errorf("expected 1024 dims, got %d", e.Dimensions())
	}
}

func TestCascadeTier0InheritsAPICredentials(t *testing.T) {
	// Override with model but no api_key should inherit from top-level api config
	ov := &EmbedOverride{
		Model:      "custom-embed-model",
		Dimensions: 768,
	}
	e := NewCascade("openai", "sk-from-api", "https://api.example.com/v1", ov)
	if e == nil {
		t.Fatal("expected embedder inheriting api credentials")
	}
	if e.Name() != "openai/custom-embed-model" {
		t.Errorf("expected override model, got: %s", e.Name())
	}
	if e.Dimensions() != 768 {
		t.Errorf("expected 768 dims, got %d", e.Dimensions())
	}
}

func TestCascadeTier0AutoDetectDimensions(t *testing.T) {
	// Unknown model with no explicit dimensions should start at 0 (auto-detect)
	ov := &EmbedOverride{
		Model:  "qwen/qwen3-embedding-8b",
		APIKey: "sk-test",
	}
	e := NewCascade("openai", "sk-test", "", ov)
	if e == nil {
		t.Fatal("expected embedder")
	}
	// Dimensions are 0 until first embed call auto-detects
	if e.Dimensions() != 0 {
		t.Errorf("expected 0 (auto-detect), got %d", e.Dimensions())
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

func TestEmbedRetry429ThenSuccess(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error": "rate limited"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1, 2, 3}}},
		})
	}))
	defer server.Close()

	e := &APIEmbedder{provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3}
	vec, err := e.Embed("test")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3 dims, got %d", len(vec))
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", calls)
	}
}

func TestEmbedRetryExhausted429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer server.Close()

	e := &APIEmbedder{provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3}
	_, err := e.Embed("test")
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if !llm.IsRateLimitError(err) {
		t.Errorf("expected RateLimitError, got: %T %v", err, err)
	}
}

func TestEmbedRetry503ThenSuccess(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`service unavailable`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{4, 5, 6}}},
		})
	}))
	defer server.Close()

	e := &APIEmbedder{provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3}
	vec, err := e.Embed("test")
	if err != nil {
		t.Fatalf("expected success after 503 retry, got: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3 dims, got %d", len(vec))
	}
}

func TestEmbedRetryExhausted503(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`service unavailable`))
	}))
	defer server.Close()

	e := &APIEmbedder{provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3}
	_, err := e.Embed("test")
	if err == nil {
		t.Fatal("expected error after 503 retry exhaustion")
	}
	if llm.IsRateLimitError(err) {
		t.Error("503 exhaustion should NOT be RateLimitError")
	}
}

func TestEmbedNoRetryOn400(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		w.Write([]byte(`bad request`))
	}))
	defer server.Close()

	e := &APIEmbedder{provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3}
	_, err := e.Embed("test")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 call (no retry for 400), got %d", calls)
	}
}

func TestEmbedRateLimiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{1, 2, 3}}},
		})
	}))
	defer server.Close()

	e := &APIEmbedder{
		provider: "openai", model: "x", apiKey: "sk", baseURL: server.URL, dims: 3,
		limiter: newEmbedLimiter(60), // 1 per second
	}

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := e.Embed("test"); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 1900*time.Millisecond {
		t.Errorf("expected ≥2s for 3 calls at 1/sec, got %v", elapsed)
	}
}
