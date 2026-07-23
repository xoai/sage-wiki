package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAIDegradeEndToEnd proves the degrade chain against the REAL
// openaiProvider: json_schema 400-with-field-mention → exactly ONE retry
// whose body uses json_object (not json_schema) with the envelope for
// array sites → success.
func TestOpenAIDegradeEndToEnd(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error": {"message": "Invalid schema for response_format: minItems is not permitted"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"items": [{"id": 1, "score": 0.5}]}`}}},
			"model": "gpt-test",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	client, err := NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatal(err)
	}
	schema := JSONSchema{Name: "rerank", IsArray: true, Schema: map[string]any{
		"type": "array", "minItems": 1,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"id":    map[string]any{"type": "integer"},
			"score": map[string]any{"type": "number"},
		}, "required": []string{"id", "score"}},
	}}
	payload, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("degrade chain failed: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected exactly 2 requests (initial + one degrade), got %d", len(bodies))
	}
	rf0 := bodies[0]["response_format"].(map[string]any)
	if rf0["type"] != "json_schema" {
		t.Errorf("first request must use json_schema, got %v", rf0["type"])
	}
	rf1 := bodies[1]["response_format"].(map[string]any)
	if rf1["type"] != "json_object" {
		t.Errorf("degrade retry must use json_object, got %v", rf1["type"])
	}
	if string(payload) != `[{"id": 1, "score": 0.5}]` {
		t.Errorf("payload = %s", payload)
	}
}

// TestAnthropicExtraParamsStructured protects the forced tool from
// extra_params merges (thinking config must not clobber tools/tool_choice).
func TestAnthropicExtraParamsStructured(t *testing.T) {
	p := newAnthropicProvider("k", "https://api.test")
	setter, ok := any(p).(extraParamsSetter)
	if !ok {
		t.Skip("anthropic has no extraParamsSetter")
	}
	setter.setExtraParams(map[string]any{
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 2000},
	})
	build, ok, err := p.FormatStructuredRequest(
		[]Message{{Role: "user", Content: "x"}},
		testSchema(), CallOpts{Model: "claude-test"})
	if err != nil || !ok {
		t.Fatalf("FormatStructuredRequest: %v %v", err, ok)
	}
	req, _ := build()
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	if body["tools"] == nil || body["tool_choice"] == nil {
		t.Error("extra_params clobbered the forced tool (protected keys violated)")
	}
	if body["thinking"] == nil {
		t.Error("thinking config not merged (extra_params dropped)")
	}
}
