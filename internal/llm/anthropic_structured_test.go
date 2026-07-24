package llm

import (
	"encoding/json"
	"testing"
)

func testSchema() JSONSchema {
	return JSONSchema{
		Name:        "concepts",
		Description: "extracted concepts",
		IsArray:     true,
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"type":    map[string]any{"type": "string"},
				},
				"required": []string{"name", "sources", "type"},
			},
			"minItems": 0,
		},
	}
}

func TestAnthropicStructuredRequestShape(t *testing.T) {
	p := &anthropicProvider{apiKey: "k"}
	build, ok, err := p.FormatStructuredRequest(
		[]Message{{Role: "user", Content: "extract"}},
		testSchema(), CallOpts{Model: "claude-test", MaxTokens: 100})
	if err != nil || !ok {
		t.Fatalf("FormatStructuredRequest: %v %v", err, ok)
	}
	req, err := build()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "concepts" {
		t.Errorf("tool name = %v", tool["name"])
	}
	inputSchema := tool["input_schema"].(map[string]any)
	if inputSchema["type"] != "object" {
		t.Errorf("input_schema must be envelope object, got %v", inputSchema["type"])
	}
	if inputSchema["additionalProperties"] != false {
		t.Error("envelope must set additionalProperties: false")
	}
	tc := body["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "concepts" {
		t.Errorf("tool_choice = %v", tc)
	}
}

func TestAnthropicStructuredParse(t *testing.T) {
	p := &anthropicProvider{}
	body := []byte(`{
		"content": [
			{"type": "text", "text": ""},
			{"type": "tool_use", "id": "tu_1", "name": "concepts",
			 "input": {"items": [{"name": "caching", "sources": ["doc.md"], "type": "technique"}]}}
		],
		"model": "claude-test",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`)
	payload, err := p.ParseStructuredResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 1 || env.Items[0]["name"] != "caching" {
		t.Errorf("payload = %s", payload)
	}

	// No tool_use block → error.
	if _, err := p.ParseStructuredResponse([]byte(`{"content": [{"type": "text", "text": "hi"}]}`)); err == nil {
		t.Error("expected error for missing tool_use")
	}
}
