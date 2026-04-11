package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request format
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("missing auth header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content-type")
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		if body["model"] != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %v", body["model"])
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "Hello from OpenAI"}},
			},
			"model": "gpt-4o",
			"usage": map[string]int{"total_tokens": 42},
		})
	}))
	defer server.Close()

	client, err := NewClient("openai", "sk-test", server.URL, 1000)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.ChatCompletion([]Message{
		{Role: "user", Content: "Hello"},
	}, CallOpts{Model: "gpt-4o", MaxTokens: 100})

	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != "Hello from OpenAI" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if resp.TokensUsed != 42 {
		t.Errorf("expected 42 tokens, got %d", resp.TokensUsed)
	}
}

func TestAnthropicFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Error("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("missing anthropic-version header")
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		// Verify system message is separate
		if body["system"] == nil {
			t.Error("expected system field for Anthropic")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "Hello from Claude"},
			},
			"model": "claude-sonnet-4-20250514",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()

	client, err := NewClient("anthropic", "sk-ant-test", server.URL, 1000)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.ChatCompletion([]Message{
		{Role: "system", Content: "You are a wiki compiler."},
		{Role: "user", Content: "Summarize this."},
	}, CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 2000})

	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != "Hello from Claude" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if resp.TokensUsed != 30 {
		t.Errorf("expected 30 tokens, got %d", resp.TokensUsed)
	}
}

func TestGeminiFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "generateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "gemini-key" {
			t.Error("missing API key in query")
		}

		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{
					"parts": []map[string]string{
						{"text": "Hello from Gemini"},
					},
				}},
			},
			"usageMetadata":  map[string]int{"totalTokenCount": 15},
			"modelVersion": "gemini-2.5-flash",
		})
	}))
	defer server.Close()

	client, err := NewClient("gemini", "gemini-key", server.URL, 1000)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.ChatCompletion([]Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello"},
	}, CallOpts{Model: "gemini-2.5-flash"})

	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != "Hello from Gemini" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestRetryOn429(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count <= 2 {
			w.WriteHeader(429)
			w.Write([]byte("rate limited"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "success after retry"}},
			},
			"model": "gpt-4o",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	client, err := NewClient("openai", "sk-test", server.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ChatCompletion([]Message{
		{Role: "user", Content: "test"},
	}, CallOpts{Model: "gpt-4o"})

	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if resp.Content != "success after retry" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", callCount)
	}
}

func TestRateLimiter(t *testing.T) {
	limiter := newRateLimiter(600) // 10 per second
	start := time.Now()

	for i := 0; i < 5; i++ {
		limiter.wait()
	}

	elapsed := time.Since(start)
	// 5 calls at 600 RPM = 100ms interval, ~400ms minimum
	if elapsed < 350*time.Millisecond {
		t.Errorf("rate limiter too fast: %v", elapsed)
	}
}

func TestUnsupportedProvider(t *testing.T) {
	_, err := NewClient("invalid-provider", "key", "", 0)
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestBackoffDelay(t *testing.T) {
	d0 := backoffDelay(0)
	d3 := backoffDelay(3)

	if d0 > 3*time.Second {
		t.Errorf("attempt 0 delay too large: %v", d0)
	}
	if d3 > 60*time.Second {
		t.Errorf("attempt 3 should be capped at 60s, got %v", d3)
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []int{429, 500, 502, 503}
	for _, code := range retryable {
		if !isRetryable(code) {
			t.Errorf("expected %d to be retryable", code)
		}
	}

	notRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range notRetryable {
		if isRetryable(code) {
			t.Errorf("expected %d to NOT be retryable", code)
		}
	}
}

func TestOllamaUsesOpenAIFormat(t *testing.T) {
	client, err := NewClient("ollama", "", "", 0)
	if err != nil {
		t.Fatalf("NewClient ollama: %v", err)
	}
	if client.ProviderName() != "openai" {
		t.Errorf("ollama should use openai provider, got %s", client.ProviderName())
	}
}

func TestStripThinkTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no tags", "Hello world", "Hello world"},
		{"simple tag", "<think>reasoning</think>Answer", "Answer"},
		{"multiline tag", "<think>\nstep 1\nstep 2\n</think>\nResult", "Result"},
		{"tag with trailing whitespace", "<think>internal</think>  \n\nContent here", "Content here"},
		{"multiple tags", "<think>first</think>A<think>second</think>B", "AB"},
		{"no think tags just content", "plain text without tags", "plain text without tags"},
		// Fallback: when strip produces empty, extract think content
		{"fallback single think", "<think>only reasoning</think>", "only reasoning"},
		{"fallback with whitespace", "<think>  content  </think>   ", "content"},
		{"fallback multiline think", "<think>\nline 1\nline 2\n</think>", "line 1\nline 2"},
		{"fallback first of multiple", "<think>first</think><think>second</think>", "first"},
		// Edge cases
		{"empty string", "", ""},
		{"just whitespace", "   ", ""},
		{"nested angle brackets", "<think>a<b>c</b>d</think>Real", "Real"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripThinkTags(tt.in)
			if got != tt.want {
				t.Errorf("stripThinkTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChatCompletionStripsThinkTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "<think>I need to figure this out</think>\nThe answer is 42."}},
			},
			"model": "deepseek-v3",
			"usage": map[string]int{"total_tokens": 50},
		})
	}))
	defer server.Close()

	client, err := NewClient("openai", "sk-test", server.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ChatCompletion([]Message{
		{Role: "user", Content: "test"},
	}, CallOpts{Model: "deepseek-v3"})

	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if strings.Contains(resp.Content, "<think>") {
		t.Errorf("think tags not stripped: %q", resp.Content)
	}
	if resp.Content != "The answer is 42." {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}
