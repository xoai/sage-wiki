package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// anthropicProvider implements the Anthropic Messages API format.
type anthropicProvider struct {
	apiKey  string
	baseURL string
}

func newAnthropicProvider(apiKey string, baseURL string) *anthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &anthropicProvider{apiKey: apiKey, baseURL: baseURL}
}

func (p *anthropicProvider) Name() string        { return "anthropic" }
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
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}

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
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}

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
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("anthropic: parse: %w", err)
	}

	if len(result.Content) == 0 {
		return nil, fmt.Errorf("anthropic: empty content in response")
	}

	var text string
	for _, block := range result.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return &Response{
		Content:    text,
		Model:      result.Model,
		TokensUsed: result.Usage.InputTokens + result.Usage.OutputTokens,
		Usage: Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			CachedTokens: result.Usage.CacheReadInputTokens,
		},
	}, nil
}
