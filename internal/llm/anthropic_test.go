package llm

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestAnthropicParseResponse_ThinkingOnlyTruncation is the reproducing test for
// the "LLM returned empty content" bug: DeepSeek via the Anthropic protocol
// returns a thinking-only content block with stop_reason=max_tokens (it exhausts
// the output budget on chain-of-thought). The parser must surface FinishReason
// and Reasoning so EmptyContentDetails can give an actionable hint.
func TestAnthropicParseResponse_ThinkingOnlyTruncation(t *testing.T) {
	body := []byte(`{
		"content": [{"type":"thinking","thinking":"let me reason about this at length"}],
		"stop_reason": "max_tokens",
		"model": "deepseek-via-anthropic",
		"usage": {"input_tokens": 1000, "output_tokens": 500}
	}`)

	p := newAnthropicProvider("k", "")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty", resp.Content)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want %q (stop_reason max_tokens normalized)", resp.FinishReason, "length")
	}
	if !strings.Contains(resp.Reasoning, "reason about this") {
		t.Errorf("Reasoning should capture the thinking block; got %q", resp.Reasoning)
	}

	details := resp.EmptyContentDetails()
	for _, want := range []string{"finish_reason=length", "reasoning consumed", "output_tokens=500", "summary_max_tokens"} {
		if !strings.Contains(details, want) {
			t.Errorf("EmptyContentDetails missing %q\nfull: %s", want, details)
		}
	}
}

func TestAnthropicParseResponse_TextBlock(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","model":"m","usage":{"input_tokens":5,"output_tokens":3}}`)
	p := newAnthropicProvider("k", "")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop (end_turn normalized)", resp.FinishReason)
	}
}

// TestAnthropicParseResponse_EmptyArrayRoutesToActionable: a truly-empty content
// array now returns a Response (with FinishReason) routed through the actionable
// empty-content path, not a bare "empty content in response" error.
func TestAnthropicParseResponse_EmptyArrayRoutesToActionable(t *testing.T) {
	body := []byte(`{"content":[],"stop_reason":"max_tokens","model":"m","usage":{"input_tokens":1000,"output_tokens":500}}`)
	p := newAnthropicProvider("k", "")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("empty array should not error now: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty", resp.Content)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", resp.FinishReason)
	}
}

func TestAnthropicFormatBody_MergesExtraParams(t *testing.T) {
	p := newAnthropicProvider("k", "")
	p.setExtraParams(map[string]interface{}{
		"thinking": map[string]any{"type": "disabled"},
		"model":    "HACK", // protected — must not override
	})
	body, _ := p.formatBody([]Message{{Role: "user", Content: "hi"}}, CallOpts{Model: "real-model", MaxTokens: 100}, false)
	if _, ok := body["thinking"]; !ok {
		t.Error("thinking extra param was not merged into the body")
	}
	if body["model"] != "real-model" {
		t.Errorf("structural key 'model' was overridden by extra_params: %v", body["model"])
	}
}

// TestExtraParamsReachProviders proves extra_params survive the nonBatchProvider
// wrapper (openai-compatible/qwen) AND reach the anthropic provider body — the
// wiring gap that left the documented enable_thinking/reasoning_effort remedy
// unreachable for the most common DeepSeek setups.
func TestExtraParamsReachProviders(t *testing.T) {
	for _, prov := range []string{"openai-compatible", "qwen", "anthropic"} {
		c, err := NewClient(prov, "k", "http://example.test", -1, map[string]interface{}{"reasoning_effort": "low"})
		if err != nil {
			t.Fatalf("%s: NewClient: %v", prov, err)
		}
		req, err := c.provider.FormatRequest([]Message{{Role: "user", Content: "hi"}}, CallOpts{Model: "m", MaxTokens: 100})
		if err != nil {
			t.Fatalf("%s: FormatRequest: %v", prov, err)
		}
		b, _ := io.ReadAll(req.Body)
		var body map[string]interface{}
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("%s: body not JSON: %v", prov, err)
		}
		if body["reasoning_effort"] != "low" {
			t.Errorf("%s: extra_params did not reach request body: %v", prov, body)
		}
	}
}

// TestAnthropicParseResponse_CacheWriteTokens verifies cache creation tokens
// land in Usage.CacheWriteTokens instead of being discarded, and cache read
// tokens remain CachedTokens. SPEC-05.
func TestAnthropicParseResponse_CacheWriteTokens(t *testing.T) {
	body := []byte(`{
		"content": [{"type": "text", "text": "ok"}],
		"stop_reason": "end_turn",
		"model": "claude-sonnet-4-5",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 20,
			"cache_creation_input_tokens": 30,
			"cache_read_input_tokens": 50
		}
	}`)
	p := &anthropicProvider{}
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 30 {
		t.Errorf("CacheWriteTokens = %d, want 30 (cache_creation_input_tokens)", resp.Usage.CacheWriteTokens)
	}
	if resp.Usage.CachedTokens != 50 {
		t.Errorf("CachedTokens = %d, want 50 (cache_read_input_tokens)", resp.Usage.CachedTokens)
	}
	// InputTokens is normalized to total prompt tokens INCLUDING cached
	// (anthropic's raw input_tokens excludes cache_read), matching the
	// openai-style semantics the cost formula expects.
	if resp.Usage.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want 150 (100 uncached + 50 cache_read)", resp.Usage.InputTokens)
	}
}
