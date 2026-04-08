package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// --- CachingProvider interface tests ---

func TestCachingProviderInterface(t *testing.T) {
	// Verify both providers implement CachingProvider
	var _ CachingProvider = &anthropicProvider{}
	var _ CachingProvider = &geminiProvider{}
}

// --- Anthropic caching tests ---

func TestAnthropicFormatCachedRequest(t *testing.T) {
	p := newAnthropicProvider("test-key", "http://localhost")

	msgs := []Message{
		{Role: "system", Content: "You are a wiki compiler."},
		{Role: "user", Content: "Summarize this."},
	}
	opts := CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 1024}

	req, err := p.FormatCachedRequest("", msgs, opts)
	if err != nil {
		t.Fatalf("FormatCachedRequest: %v", err)
	}

	// Parse the request body to verify cache_control is present
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)

	system, ok := body["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatal("expected system array in cached request body")
	}

	block, ok := system[0].(map[string]any)
	if !ok {
		t.Fatal("expected system block to be a map")
	}

	cc, ok := block["cache_control"]
	if !ok {
		t.Fatal("expected cache_control in system block")
	}
	ccMap, ok := cc.(map[string]any)
	if !ok {
		t.Fatal("expected cache_control to be a map")
	}
	if ccMap["type"] != "ephemeral" {
		t.Errorf("expected cache_control type ephemeral, got %v", ccMap["type"])
	}

	if block["text"] != "You are a wiki compiler." {
		t.Errorf("system text mismatch: %v", block["text"])
	}
}

func TestAnthropicSetupTeardownAreNoops(t *testing.T) {
	p := newAnthropicProvider("test-key", "http://localhost")

	cacheID, err := p.SetupCache("prompt", "model")
	if err != nil {
		t.Fatalf("SetupCache: %v", err)
	}
	if cacheID != "" {
		t.Errorf("expected empty cacheID for anthropic, got %q", cacheID)
	}

	if err := p.TeardownCache("anything"); err != nil {
		t.Fatalf("TeardownCache: %v", err)
	}
}

// --- Gemini caching tests ---

func TestGeminiCacheLifecycle(t *testing.T) {
	var created, deleted bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "cachedContents"):
			created = true
			json.NewEncoder(w).Encode(map[string]string{
				"name": "cachedContents/abc123",
			})
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "cachedContents/abc123"):
			deleted = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := newGeminiProvider("test-key", srv.URL)

	cacheID, err := p.SetupCache("You are a wiki compiler.", "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("SetupCache: %v", err)
	}
	if cacheID != "cachedContents/abc123" {
		t.Errorf("expected cachedContents/abc123, got %q", cacheID)
	}
	if !created {
		t.Error("SetupCache did not call create endpoint")
	}

	err = p.TeardownCache(cacheID)
	if err != nil {
		t.Fatalf("TeardownCache: %v", err)
	}
	if !deleted {
		t.Error("TeardownCache did not call delete endpoint")
	}
}

func TestGeminiSetupCacheFailureFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer srv.Close()

	p := newGeminiProvider("test-key", srv.URL)

	_, err := p.SetupCache("prompt", "model")
	if err == nil {
		t.Fatal("expected error on cache setup failure")
	}
	// Verify the error doesn't contain the API key
	if strings.Contains(err.Error(), "test-key") {
		t.Error("error message contains API key — should be sanitized")
	}
}

func TestGeminiFormatCachedRequest(t *testing.T) {
	p := newGeminiProvider("test-key", "http://localhost")

	msgs := []Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello"},
	}
	opts := CallOpts{Model: "gemini-2.5-flash"}

	req, err := p.FormatCachedRequest("cachedContents/abc123", msgs, opts)
	if err != nil {
		t.Fatalf("FormatCachedRequest: %v", err)
	}

	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)

	// Should have cachedContent set
	if body["cachedContent"] != "cachedContents/abc123" {
		t.Errorf("expected cachedContent field, got %v", body["cachedContent"])
	}

	// Should NOT have systemInstruction (it's in the cache)
	if body["systemInstruction"] != nil {
		t.Error("cached request should not include systemInstruction")
	}
}

func TestGeminiFormatCachedRequestEmptyID(t *testing.T) {
	p := newGeminiProvider("test-key", "http://localhost")

	msgs := []Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello"},
	}
	opts := CallOpts{Model: "gemini-2.5-flash"}

	// Empty cacheID should fall back to regular request
	req, err := p.FormatCachedRequest("", msgs, opts)
	if err != nil {
		t.Fatalf("FormatCachedRequest: %v", err)
	}

	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)

	// Should NOT have cachedContent
	if body["cachedContent"] != nil {
		t.Error("empty cacheID should not set cachedContent")
	}

	// Should have systemInstruction
	if body["systemInstruction"] == nil {
		t.Error("regular request should have systemInstruction")
	}
}

// --- Client-level caching tests ---

func TestChatCompletionCachedFallbackNonCachingProvider(t *testing.T) {
	// OpenAI provider doesn't implement CachingProvider (uses automatic caching)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "response via direct path"}},
			},
			"model": "gpt-4o",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer srv.Close()

	client, err := NewClient("openai", "sk-test", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	// ChatCompletionCached on a non-caching provider should fall back to direct
	resp, err := client.ChatCompletionCached("some-cache-id", []Message{
		{Role: "user", Content: "test"},
	}, CallOpts{Model: "gpt-4o"})

	if err != nil {
		t.Fatalf("expected fallback to direct, got error: %v", err)
	}
	if resp.Content != "response via direct path" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestChatCompletionCachedFallbackOnHTTPError(t *testing.T) {
	var callCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			// First call (cached path) returns error
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("server error"))
			return
		}
		// Fallback call (direct path) succeeds
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "recovered via fallback"},
			},
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	client, err := NewClient("anthropic", "sk-test", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ChatCompletionCached("", []Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "test"},
	}, CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 1024})

	if err != nil {
		t.Fatalf("expected fallback recovery, got error: %v", err)
	}
	if resp.Content != "recovered via fallback" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestClientSetupCacheNonCachingProvider(t *testing.T) {
	// OpenAI doesn't implement CachingProvider — SetupCache should be a no-op
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	client, err := NewClient("openai", "sk-test", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	cacheID, err := client.SetupCache("prompt", "model")
	if err != nil {
		t.Fatalf("SetupCache should not error: %v", err)
	}
	if cacheID != "" {
		t.Errorf("expected empty cacheID for non-caching provider, got %q", cacheID)
	}
}

func TestClientAutoRoutingThroughCache(t *testing.T) {
	// When cacheID is set on the client, ChatCompletion should auto-route through cached path
	var requestCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		// Check if this is a cached request (Anthropic: has cache_control in body)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		sys, hasSys := body["system"]
		if hasSys {
			sysArr, ok := sys.([]any)
			if ok && len(sysArr) > 0 {
				block, _ := sysArr[0].(map[string]any)
				if _, hasCC := block["cache_control"]; !hasCC {
					t.Error("expected cache_control in system block when cacheID is active")
				}
			}
		}

		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "cached response"},
			},
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	client, err := NewClient("anthropic", "sk-test", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate setting a cacheID (as SetupCache would do for anthropic — returns "")
	// For Anthropic, cacheID is empty but the provider still implements CachingProvider
	client.cacheID = "active"

	resp, err := client.ChatCompletion([]Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello"},
	}, CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 1024})

	if err != nil {
		t.Fatalf("ChatCompletion with cache: %v", err)
	}
	if resp.Content != "cached response" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestTeardownClearsClientCacheID(t *testing.T) {
	client := &Client{
		provider: newAnthropicProvider("key", "http://localhost"),
		limiter:  newRateLimiter(1000),
		cacheID:  "some-cache",
	}

	client.TeardownCache("some-cache")

	if client.cacheID != "" {
		t.Errorf("expected cacheID cleared after teardown, got %q", client.cacheID)
	}
}

// --- SSRF validation tests ---

func TestValidateBatchURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		allowedHost string
		wantErr     bool
	}{
		{"valid anthropic", "https://api.anthropic.com/v1/messages/batches/abc/results", "api.anthropic.com", false},
		{"wrong host", "https://evil.com/steal-key", "api.anthropic.com", true},
		{"http allowed for test", "http://localhost:1234/results", "localhost:1234", false},
		{"ftp scheme", "ftp://api.anthropic.com/results", "api.anthropic.com", true},
		{"empty url", "", "api.anthropic.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBatchURL(tt.url, tt.allowedHost)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBatchURL(%q, %q) error = %v, wantErr %v", tt.url, tt.allowedHost, err, tt.wantErr)
			}
		})
	}
}

// --- Gemini error sanitization test ---

func TestSanitizeGeminiError(t *testing.T) {
	input := `https://generativelanguage.googleapis.com/v1beta/cachedContents?key=AIzaSyB_SECRET_KEY123 returned error`
	result := sanitizeGeminiError(input)

	if strings.Contains(result, "AIzaSyB_SECRET_KEY123") {
		t.Error("sanitized output still contains API key")
	}
	if !strings.Contains(result, "key=REDACTED") {
		t.Errorf("expected key=REDACTED, got: %s", result)
	}
}
