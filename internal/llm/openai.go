package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// openaiProvider implements the OpenAI-compatible API format.
type openaiProvider struct {
	apiKey      string
	baseURL     string
	extraParams map[string]interface{} // provider-specific params merged into request body
}

func newOpenAIProvider(apiKey string, baseURL string) *openaiProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &openaiProvider{apiKey: apiKey, baseURL: baseURL}
}

func (p *openaiProvider) Name() string        { return "openai" }
func (p *openaiProvider) SupportsVision() bool { return true }

func (p *openaiProvider) formatBody(messages []Message, opts CallOpts, stream bool) map[string]any {
	var apiMessages []any
	for _, m := range messages {
		if m.ImageBase64 != "" {
			apiMessages = append(apiMessages, map[string]any{
				"role": m.Role,
				"content": []map[string]any{
					{"type": "text", "text": m.Content},
					{"type": "image_url", "image_url": map[string]string{
						"url": "data:" + m.ImageMime + ";base64," + m.ImageBase64,
					}},
				},
			})
		} else {
			apiMessages = append(apiMessages, map[string]string{
				"role": m.Role, "content": m.Content,
			})
		}
	}

	body := map[string]any{
		"model":    opts.Model,
		"messages": apiMessages,
	}
	body["stream"] = stream
	if opts.MaxTokens > 0 {
		body["max_tokens"] = opts.MaxTokens
	}
	if opts.Temperature > 0 {
		body["temperature"] = opts.Temperature
	}
	// Merge provider-specific extra params (e.g., enable_thinking, reasoning_effort).
	// Protected keys cannot be overridden — they are structural to the request.
	protected := map[string]bool{"model": true, "messages": true, "stream": true}
	for k, v := range p.extraParams {
		if protected[k] {
			continue
		}
		body[k] = v
	}
	return body
}

func (p *openaiProvider) makeRequest(body map[string]any) (*http.Request, error) {
	req, err := http.NewRequest("POST", p.baseURL+"/chat/completions", jsonBody(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return req, nil
}

func (p *openaiProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.makeRequest(p.formatBody(messages, opts, false))
}

func (p *openaiProvider) FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.makeRequest(p.formatBody(messages, opts, true))
}

func (p *openaiProvider) ParseStreamChunk(data []byte) (string, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", false
	}
	if len(chunk.Choices) == 0 {
		return "", false
	}
	done := chunk.Choices[0].FinishReason != nil
	return chunk.Choices[0].Delta.Content, done
}

func (p *openaiProvider) ParseResponse(body []byte) (*Response, error) {
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("openai: parse: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices in response")
	}

	return &Response{
		Content:    result.Choices[0].Message.Content,
		Model:      result.Model,
		TokensUsed: result.Usage.TotalTokens,
		Usage: Usage{
			InputTokens:  result.Usage.PromptTokens,
			OutputTokens: result.Usage.CompletionTokens,
			CachedTokens: result.Usage.PromptTokensDetails.CachedTokens,
		},
	}, nil
}
