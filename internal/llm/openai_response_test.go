package llm

import (
	"strings"
	"testing"
)

// TestOpenAIParseResponse_ReasoningAndFinishReason verifies that the OpenAI
// parser extracts the reasoning field (from DeepSeek/Qwen-style reasoning
// models) and the finish_reason — used for diagnostics when content is empty.
// Issue #85.
func TestOpenAIParseResponse_ReasoningAndFinishReason(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"content": "",
				"reasoning": "Thinking Process:\n1. Analyze\n2. Conclude"
			},
			"finish_reason": "length"
		}],
		"model": "deepseek-v4-flash",
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`)

	p := newOpenAIProvider("test-key", "https://test.example.com/v1")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}

	if resp.Content != "" {
		t.Errorf("Content = %q, want empty", resp.Content)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "length")
	}
	if !strings.Contains(resp.Reasoning, "Thinking Process") {
		t.Errorf("Reasoning should contain extracted text; got %q", resp.Reasoning)
	}
}

// TestOpenAIParseResponse_ReasoningContentField verifies the parser also reads
// DeepSeek's native OpenAI-compatible reasoning field name `reasoning_content`
// (not just `reasoning`), so reasoning-truncation diagnostics work for
// DeepSeek-via-OpenAI too.
func TestOpenAIParseResponse_ReasoningContentField(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {"content": "", "reasoning_content": "deepseek thinking here"},
			"finish_reason": "length"
		}],
		"model": "deepseek-chat",
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)
	p := newOpenAIProvider("k", "https://x/v1")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Reasoning, "deepseek thinking") {
		t.Errorf("Reasoning should read reasoning_content; got %q", resp.Reasoning)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q, want length", resp.FinishReason)
	}
}

func TestOpenAIParseResponse_NormalResponse(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {"content": "Hello world"},
			"finish_reason": "stop"
		}],
		"model": "gpt-4",
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)

	p := newOpenAIProvider("test-key", "https://api.openai.com/v1")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}

	if resp.Content != "Hello world" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Reasoning != "" {
		t.Errorf("Reasoning should be empty for non-reasoning models; got %q", resp.Reasoning)
	}
}

// TestEmptyContentDetails verifies the diagnostic message includes
// finish_reason and reasoning size when present, and gives an actionable
// hint for reasoning-model truncation.
func TestEmptyContentDetails(t *testing.T) {
	tests := []struct {
		name        string
		resp        *Response
		wantEmpty   bool     // true if details should be ""
		wantContain []string // substrings expected in details
	}{
		{
			name:      "nil response",
			resp:      nil,
			wantEmpty: true,
		},
		{
			name:      "non-empty content returns empty details",
			resp:      &Response{Content: "ok"},
			wantEmpty: true,
		},
		{
			name: "length truncation includes hint about extra_params",
			resp: &Response{
				FinishReason: "length",
				Reasoning:    "step 1, step 2, step 3",
				Usage:        Usage{OutputTokens: 200},
			},
			wantContain: []string{
				"finish_reason=length",
				"reasoning consumed",
				"output_tokens=200",
				"enable_thinking",
				"summary_max_tokens",
			},
		},
		{
			name: "natural stop with no content still mentions finish_reason",
			resp: &Response{
				FinishReason: "stop",
			},
			wantContain: []string{
				"finish_reason=stop",
			},
		},
		{
			name: "no finish reason at all still says empty",
			resp: &Response{},
			wantContain: []string{
				"LLM returned empty content",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.resp.EmptyContentDetails()
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty details, got %q", got)
				}
				return
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("details missing %q\nfull message: %s", want, got)
				}
			}
		})
	}
}

// TestOpenAIParseResponse_DeepSeekCacheSplit verifies DeepSeek's native
// cache fields (prompt_cache_hit_tokens / prompt_cache_miss_tokens) are
// parsed into the Usage split: Cached=hit, Input=hit+miss. SPEC-05.
func TestOpenAIParseResponse_DeepSeekCacheSplit(t *testing.T) {
	body := []byte(`{
		"choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}],
		"model": "deepseek-chat",
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 20,
			"total_tokens": 120,
			"prompt_cache_hit_tokens": 70,
			"prompt_cache_miss_tokens": 30
		}
	}`)
	p := newOpenAIProvider("k", "https://x/v1")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Usage.CachedTokens != 70 {
		t.Errorf("CachedTokens = %d, want 70 (prompt_cache_hit_tokens)", resp.Usage.CachedTokens)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (hit+miss)", resp.Usage.InputTokens)
	}
}

// TestOpenAIParseResponse_CachedTokensFallback verifies the standard
// prompt_tokens_details.cached_tokens path still works when DeepSeek-native
// fields are absent. SPEC-05.
func TestOpenAIParseResponse_CachedTokensFallback(t *testing.T) {
	body := []byte(`{
		"choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}],
		"model": "gpt-4o",
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 20,
			"total_tokens": 120,
			"prompt_tokens_details": {"cached_tokens": 40}
		}
	}`)
	p := newOpenAIProvider("k", "https://x/v1")
	resp, err := p.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Usage.CachedTokens != 40 {
		t.Errorf("CachedTokens = %d, want 40", resp.Usage.CachedTokens)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.Usage.InputTokens)
	}
}
