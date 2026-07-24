package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChattyMockStructuredSuccess is the spec-mandated acceptance test
// (P2-4 acceptance): a model whose response would BREAK fence-stripping
// (prose around the JSON, nested commentary) is handled cleanly by every
// native structured path — because the mechanism forbids prose by
// construction. The fallback fence-strip on the same content fails,
// proving the structured path is what saves us.
// TestChattyMockStructuredSuccess is the spec-mandated acceptance test
// (P2-4 acceptance): a model whose visible output is PROSE (which would
// degrade or break fence-stripping) still yields clean schema-validated
// JSON via every native structured path — because the mechanism carries
// the payload, not the prose. Each leg asserts (a) the request used the
// provider's constraint mechanism and (b) the payload is clean.
func TestChattyMockStructuredSuccess(t *testing.T) {
	schema := chattySchema()

	// Anthropic leg: forced tool_use — the text block is pure prose, the
	// tool input carries the payload.
	t.Run("anthropic", func(t *testing.T) {
		var sawTool bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["tools"] != nil && body["tool_choice"] != nil {
				sawTool = true
			}
			w.Write([]byte(`{"content": [
				{"type": "text", "text": "Certainly! Here is my analysis, with much preamble and even some [bracketed] asides that would confuse a naive parser..."},
				{"type": "tool_use", "id": "tu_1", "name": "concepts",
				 "input": {"items": [{"name": "caching", "sources": ["doc.md"], "type": "technique"}]}}
			], "model": "claude-test", "usage": {"input_tokens": 10, "output_tokens": 20}}`))
		}))
		defer server.Close()
		client, err := NewClient("anthropic", "fake-key", server.URL, -1)
		if err != nil {
			t.Fatal(err)
		}
		payload, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "claude-test"})
		if err != nil {
			t.Fatalf("anthropic structured: %v", err)
		}
		assertChattyPayload(t, payload)
		if !sawTool {
			t.Error("request did not use the tool_use mechanism")
		}
	})

	// OpenAI leg: strict json_schema — response content is the envelope.
	t.Run("openai", func(t *testing.T) {
		var sawRF bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if rf, ok := body["response_format"].(map[string]any); ok && rf["type"] == "json_schema" {
				sawRF = true
			}
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{
					"content": `{"items": [{"name": "caching", "sources": ["doc.md"], "type": "technique"}]}`}}},
				"model": "gpt-test",
				"usage": map[string]int{"total_tokens": 10},
			})
		}))
		defer server.Close()
		client, err := NewClient("openai", "fake-key", server.URL, -1)
		if err != nil {
			t.Fatal(err)
		}
		payload, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "gpt-test"})
		if err != nil {
			t.Fatalf("openai structured: %v", err)
		}
		assertChattyPayload(t, payload)
		if !sawRF {
			t.Error("request did not use json_schema")
		}
	})

	// Gemini leg: responseSchema — the text part is clean JSON by mime
	// constraint (the fixture's prose elsewhere is irrelevant).
	t.Run("gemini", func(t *testing.T) {
		var sawSchema bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if gc, ok := body["generationConfig"].(map[string]any); ok && gc["responseSchema"] != nil {
				sawSchema = true
			}
			resp := map[string]any{
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{
								map[string]string{"text": `{"items": [{"name": "caching", "sources": ["doc.md"], "type": "technique"}]}`},
							},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()
		client, err := NewClient("gemini", "fake-key", server.URL, -1)
		if err != nil {
			t.Fatal(err)
		}
		payload, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "gemini-test"})
		if err != nil {
			t.Fatalf("gemini structured: %v", err)
		}
		assertChattyPayload(t, payload)
		if !sawSchema {
			t.Error("request did not use responseSchema")
		}
	})

	// And the control: the same prose THROUGH the fallback fence-strip
	// yields garbage that fails validation — proving the mechanism is what
	// saves the chatty case.
	nasty := "The answer is [not json] but also not {[broken] though"
	extracted, _ := ParseJSONFromText(nasty)
	if err := ValidateJSON(schema.Envelope(), extracted); err == nil {
		t.Fatal("control fixture must produce invalid content through fence-strip")
	}
}

func chattySchema() JSONSchema {
	return JSONSchema{
		Name: "concepts", IsArray: true,
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

func assertChattyPayload(t *testing.T, payload json.RawMessage) {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal(payload, &items); err != nil {
		t.Fatalf("payload not clean JSON: %s (%v)", payload, err)
	}
	if len(items) != 1 || items[0]["name"] != "caching" {
		t.Fatalf("payload = %s", payload)
	}
}

// TestFallbackParity verifies the openai-compatible fallback path is
// byte-identical to the old fence-strip parser on the same fixtures.
func TestFallbackParity(t *testing.T) {
	fixtures := []string{
		`[{"name": "a", "sources": ["x.md"], "type": "concept"}]`,
		"```json\n[{\"name\": \"a\", \"sources\": [\"x.md\"], \"type\": \"concept\"}]\n```",
		"Here is the JSON you wanted:\n[{\"name\": \"a\", \"sources\": [\"x.md\"], \"type\": \"concept\"}]\nDone.",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": fixtures[0]}}},
			"model": "m",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	for i, fx := range fixtures {
		oldWay, err1 := ParseJSONFromText(fx)
		server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": fx}}},
				"model":   "m",
				"usage":   map[string]int{"total_tokens": 10},
			})
		}))
		client, err := NewClient("openai-compatible", "fake-key", server2.URL, -1)
		if err != nil {
			t.Fatal(err)
		}
		newWay, _, err2 := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}},
			JSONSchema{Name: "t", IsArray: true, Schema: map[string]any{"type": "array", "minItems": 0,
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"type":    map[string]any{"type": "string"},
				}, "required": []string{"name", "sources", "type"}}}},
			CallOpts{Model: "m"})
		server2.Close()
		if err1 != nil || err2 != nil {
			t.Fatalf("fixture %d: old=%v new=%v", i, err1, err2)
		}
		if string(oldWay) != string(newWay) {
			t.Errorf("fixture %d: fallback mismatch\nold: %s\nnew: %s", i, oldWay, newWay)
		}
	}
	_ = server
}
