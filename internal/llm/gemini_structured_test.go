package llm

import (
	"encoding/json"
	"testing"
)

func TestGeminiOpenAPIMapping(t *testing.T) {
	canonical := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"aliases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"name"},
		"additionalProperties": false,
	}
	out := openAPIForm(canonical)
	if _, banned := out["additionalProperties"]; banned {
		t.Error("additionalProperties must be dropped for OpenAPI subset")
	}
	props := out["properties"].(map[string]any)
	if props["aliases"].(map[string]any)["nullable"] != true {
		t.Error("optional aliases must be nullable: true")
	}
	if props["name"].(map[string]any)["nullable"] == true {
		t.Error("required name must not be nullable")
	}
}

func TestGeminiStructuredRequestShape(t *testing.T) {
	p := &geminiProvider{apiKey: "k", baseURL: "https://gen.test"}
	build, ok, err := p.FormatStructuredRequest(
		[]Message{{Role: "user", Content: "extract"}},
		testSchema(), CallOpts{Model: "gemini-test"})
	if err != nil || !ok {
		t.Fatalf("FormatStructuredRequest: %v %v", err, ok)
	}
	req, err := build()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	gc := body["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("mime = %v", gc["responseMimeType"])
	}
	if gc["responseSchema"] == nil {
		t.Error("responseSchema missing")
	}
}

func TestGeminiStructuredParse(t *testing.T) {
	p := &geminiProvider{}
	payload, err := p.ParseStructuredResponse([]byte(`{"candidates": [{"content": {"parts": [{"text": "{\"items\": []}"}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"items": []}` {
		t.Errorf("payload = %s", payload)
	}
}
