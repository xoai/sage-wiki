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

// TestSecond400FallsBackToPlain pins spec §3: when BOTH json_schema and
// json_object are rejected (two 400s), the client falls back to a plain
// completion + fence-strip — previously-working call sites don't break on
// proxies that reject constrained modes entirely.
func TestSecond400FallsBackToPlain(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if len(bodies) <= 2 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error": {"message": "Invalid schema for response_format"}}`))
			return
		}
		// Third request: plain (no response_format) → fence-strip success.
		if _, constrained := body["response_format"]; constrained {
			t.Errorf("fallback request must NOT carry response_format")
		}
		// Fallback prompt (the site's own) asks for a bare JSON array.
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `[{"id": 1, "score": 0.5}]`}}},
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
		t.Fatalf("second-400 fallback failed: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 requests (json_schema + json_object + plain), got %d", len(bodies))
	}
	if string(payload) != `[{"id": 1, "score": 0.5}]` {
		t.Errorf("payload = %s", payload)
	}
}

// TestStructuredTrackUsagePinsCostParity pins that trackUsage fires on the
// structured path (spec test 7).
func TestStructuredTrackUsagePinsCostParity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `{"items": [{"id": 1, "score": 0.5}]}`}}},
			"model": "gpt-test",
			"usage": map[string]int{"prompt_tokens": 42, "completion_tokens": 7, "total_tokens": 49},
		})
	}))
	defer server.Close()
	client, err := NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatal(err)
	}
	tracker := mustCostTracker(t, "openai", 0)
	client.SetTracker(tracker)
	schema := JSONSchema{Name: "rerank", IsArray: true, Schema: map[string]any{
		"type": "array", "minItems": 1,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"id":    map[string]any{"type": "integer"},
			"score": map[string]any{"type": "number"},
		}, "required": []string{"id", "score"}},
	}}
	if _, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	report := tracker.Report()
	if report.TotalInputTokens != 42 || report.TotalOutputTokens != 7 {
		t.Errorf("usage not tracked on structured path: %+v", report)
	}
}

// TestContextLength400AlsoFallsBack pins spec §3: a 400 WITHOUT the field
// mention (e.g. context-length) skips the json_object degrade but still
// falls back to a plain completion (which may succeed or fail on its own).
func TestContextLength400AlsoFallsBack(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error": {"message": "This model's maximum context length is 8192 tokens"}}`))
			return
		}
		if _, constrained := body["response_format"]; constrained {
			t.Error("context-length 400 must NOT trigger the json_object degrade")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `[{"id": 1, "score": 0.5}]`}}},
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
	if _, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "gpt-test"}); err != nil {
		t.Fatalf("context-length fallback: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 requests (constrained + plain fallback), got %d", len(bodies))
	}
}
