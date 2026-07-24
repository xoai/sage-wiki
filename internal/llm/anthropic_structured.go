package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Anthropic structured outputs (P2-4): forced tool_use — a synthetic tool
// named after the schema with the ENVELOPE as input_schema (arrays need an
// object root) and tool_choice forcing it. Non-cached by design (D7).
func (p *anthropicProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	return func() (*http.Request, error) {
		body, _ := p.formatBody(messages, opts, false)
		body["tools"] = []map[string]any{{
			"name":         schema.ToolName(),
			"description":  schema.Description,
			"input_schema": schema.Envelope(),
		}}
		body["tool_choice"] = map[string]any{"type": "tool", "name": schema.ToolName()}
		return p.makeRequest(body)
	}, true, nil
}

// ParseStructuredResponse extracts the tool_use block's input from the raw
// response body (the forced tool carries the entire payload).
func (p *anthropicProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	var raw struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("anthropic structured: unmarshal: %w", err)
	}
	for _, block := range raw.Content {
		if block.Type == "tool_use" && len(block.Input) > 0 {
			return block.Input, nil
		}
	}
	return nil, fmt.Errorf("anthropic structured: no tool_use block in response")
}
