package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// anthropicProvider implements the Anthropic Messages API format.
type anthropicProvider struct {
	apiKey      string
	baseURL     string
	extraParams map[string]interface{} // provider-specific params merged into request body
}

// setExtraParams satisfies extraParamsSetter so caller-supplied extra_params
// (e.g. a `thinking` config to disable reasoning) reach the request body.
func (p *anthropicProvider) setExtraParams(m map[string]interface{}) { p.extraParams = m }

// mergeExtraParams copies provider-specific extra params into the request body,
// skipping structural keys that must not be overridden.
func mergeExtraParams(body map[string]any, extra map[string]interface{}, protected map[string]bool) {
	for k, v := range extra {
		if protected[k] {
			continue
		}
		body[k] = v
	}
}

// anthropicProtectedKeys are the structural body keys extra_params must not
// clobber (model/messages/stream/system shape the request itself).
var anthropicProtectedKeys = map[string]bool{"model": true, "messages": true, "stream": true, "system": true, "tools": true, "tool_choice": true}

// normalizeAnthropicStop maps Anthropic stop_reason values to the OpenAI-style
// finish_reason vocabulary used across providers, so EmptyContentDetails gives
// consistent diagnostics + hints. Unknown values pass through unchanged.
func normalizeAnthropicStop(stop string) string {
	switch stop {
	case "max_tokens":
		return "length"
	case "end_turn", "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	case "tool_use":
		return "tool_calls"
	default:
		return stop
	}
}

func newAnthropicProvider(apiKey string, baseURL string) *anthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &anthropicProvider{apiKey: apiKey, baseURL: baseURL}
}

func (p *anthropicProvider) Name() string         { return "anthropic" }
func (p *anthropicProvider) SupportsVision() bool { return true }

func (p *anthropicProvider) formatBody(messages []Message, opts CallOpts, stream bool) (map[string]any, string) {
	var systemPrompt string
	var apiMessages []any

	for _, m := range messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}
		if m.ImageBase64 != "" {
			apiMessages = append(apiMessages, map[string]any{
				"role": m.Role,
				"content": []map[string]any{
					{"type": "image", "source": map[string]string{
						"type":       "base64",
						"media_type": m.ImageMime,
						"data":       m.ImageBase64,
					}},
					{"type": "text", "text": m.Content},
				},
			})
		} else {
			apiMessages = append(apiMessages, map[string]string{
				"role":    m.Role,
				"content": m.Content,
			})
		}
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      opts.Model,
		"messages":   apiMessages,
		"max_tokens": maxTokens,
	}
	if stream {
		body["stream"] = true
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}
	if opts.Temperature != nil {
		body["temperature"] = *opts.Temperature
	}

	mergeExtraParams(body, p.extraParams, anthropicProtectedKeys)

	return body, systemPrompt
}

func (p *anthropicProvider) makeRequest(body map[string]any) (*http.Request, error) {
	req, err := http.NewRequest("POST", p.baseURL+"/v1/messages", jsonBody(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return req, nil
}

func (p *anthropicProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	body, _ := p.formatBody(messages, opts, false)
	return p.makeRequest(body)
}

func (p *anthropicProvider) FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	body, _ := p.formatBody(messages, opts, true)
	return p.makeRequest(body)
}

// --- CachingProvider implementation ---

func (p *anthropicProvider) SetupCache(systemPrompt string, model string) (string, error) {
	// Anthropic caching is per-request via cache_control — no setup needed
	return "", nil
}

func (p *anthropicProvider) FormatCachedRequest(cacheID string, messages []Message, opts CallOpts) (*http.Request, error) {
	var systemContent []map[string]any
	var apiMessages []any

	for _, m := range messages {
		if m.Role == "system" {
			// System message with cache_control for prompt caching
			systemContent = append(systemContent, map[string]any{
				"type":          "text",
				"text":          m.Content,
				"cache_control": map[string]string{"type": "ephemeral"},
			})
			continue
		}
		if m.ImageBase64 != "" {
			apiMessages = append(apiMessages, map[string]any{
				"role": m.Role,
				"content": []map[string]any{
					{"type": "image", "source": map[string]string{
						"type":       "base64",
						"media_type": m.ImageMime,
						"data":       m.ImageBase64,
					}},
					{"type": "text", "text": m.Content},
				},
			})
		} else {
			apiMessages = append(apiMessages, map[string]string{
				"role":    m.Role,
				"content": m.Content,
			})
		}
	}

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      opts.Model,
		"messages":   apiMessages,
		"max_tokens": maxTokens,
	}
	if len(systemContent) > 0 {
		body["system"] = systemContent
	}
	if opts.Temperature != nil {
		body["temperature"] = *opts.Temperature
	}

	mergeExtraParams(body, p.extraParams, anthropicProtectedKeys)

	return p.makeRequest(body)
}

func (p *anthropicProvider) TeardownCache(cacheID string) error {
	// Anthropic caches auto-expire (5min TTL) — no cleanup needed
	return nil
}

func (p *anthropicProvider) ParseStreamChunk(data []byte) (string, bool) {
	var chunk struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", false
	}
	switch chunk.Type {
	case "content_block_delta":
		return chunk.Delta.Text, false
	case "message_stop":
		return "", true
	}
	return "", false
}

func (p *anthropicProvider) ParseResponse(body []byte) (*Response, error) {
	var result struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Model      string `json:"model"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("anthropic: parse: %w", err)
	}

	// Extract answer text and (separately) reasoning. A reasoning model that
	// exhausts max_tokens on chain-of-thought returns a `thinking` block with no
	// `text` block; surfacing FinishReason + Reasoning lets EmptyContentDetails
	// explain the empty result instead of failing opaquely. An empty content
	// array is handled the same way (empty Content + FinishReason) rather than a
	// bare error, so callers route it through the actionable empty-content path.
	var text, reasoning string
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			text += block.Text
		case "thinking":
			reasoning += block.Thinking
		}
	}

	return &Response{
		Content:      text,
		Model:        result.Model,
		TokensUsed:   result.Usage.InputTokens + result.Usage.CacheReadInputTokens + result.Usage.CacheCreationInputTokens + result.Usage.OutputTokens,
		FinishReason: normalizeAnthropicStop(result.StopReason),
		Reasoning:    reasoning,
		Usage: Usage{
			// Normalized to openai-style semantics: InputTokens is the TOTAL
			// prompt-side token count including cache-read tokens (anthropic's
			// raw input_tokens excludes them). cache_creation stays separate
			// in CacheWriteTokens — it is billed at its own premium rate.
			InputTokens:      result.Usage.InputTokens + result.Usage.CacheReadInputTokens,
			OutputTokens:     result.Usage.OutputTokens,
			CachedTokens:     result.Usage.CacheReadInputTokens,
			CacheWriteTokens: result.Usage.CacheCreationInputTokens,
		},
	}, nil
}
