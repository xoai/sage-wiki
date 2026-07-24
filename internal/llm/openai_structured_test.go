package llm

import (
	"encoding/json"
	"testing"
)

func TestOpenAIStrictDerivation(t *testing.T) {
	canonical := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":    map[string]any{"type": "string"},
						"aliases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required": []string{"name"},
				},
				"minItems": 0,
			},
		},
		"required": []string{"items"},
	}
	out := strictForm(canonical)

	if out["additionalProperties"] != false {
		t.Error("root additionalProperties not false")
	}
	req := out["required"].([]string)
	if len(req) != 1 || req[0] != "items" {
		t.Errorf("root required = %v", req)
	}
	items := out["properties"].(map[string]any)["items"].(map[string]any)
	if _, banned := items["minItems"]; banned {
		t.Error("minItems leaked to the wire (OpenAI strict 400s on it)")
	}
	itemObj := items["items"].(map[string]any)
	if itemObj["additionalProperties"] != false {
		t.Error("nested additionalProperties not false")
	}
	nestedReq := itemObj["required"].([]string)
	if len(nestedReq) != 2 {
		t.Errorf("nested required must be ALL properties (required-all), got %v", nestedReq)
	}
	aliases := itemObj["properties"].(map[string]any)["aliases"].(map[string]any)
	typeArr, ok := aliases["type"].([]string)
	if !ok || len(typeArr) != 2 || typeArr[1] != "null" {
		t.Errorf("optional aliases must be nullable union, got %v", aliases["type"])
	}
	name := itemObj["properties"].(map[string]any)["name"].(map[string]any)
	if _, isUnion := name["type"].([]string); isUnion {
		t.Error("required name must NOT be nullable-unioned")
	}
}

func TestOpenAIStructuredRequestShape(t *testing.T) {
	p := &openaiProvider{apiKey: "k"}
	build, ok, err := p.FormatStructuredRequest(
		[]Message{{Role: "user", Content: "extract"}},
		testSchema(), CallOpts{Model: "gpt-test"})
	if err != nil || !ok {
		t.Fatalf("FormatStructuredRequest: %v %v", err, ok)
	}
	req, err := build()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	rf := body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format type = %v", rf["type"])
	}
	js := rf["json_schema"].(map[string]any)
	if js["strict"] != true {
		t.Error("strict not true")
	}
	if js["name"] != "concepts" {
		t.Errorf("schema name = %v", js["name"])
	}

	// Degraded mode.
	buildD, ok, _ := p.FormatStructuredRequest(nil, JSONSchema{Name: "x", Degraded: true, Schema: map[string]any{"type": "object"}}, CallOpts{Model: "m"})
	reqD, _ := buildD()
	var bodyD map[string]any
	json.NewDecoder(reqD.Body).Decode(&bodyD)
	if bodyD["response_format"].(map[string]any)["type"] != "json_object" {
		t.Error("degraded mode must emit json_object")
	}
}

func TestOpenAIStructuredParse(t *testing.T) {
	p := &openaiProvider{}
	payload, err := p.ParseStructuredResponse([]byte(`{"choices": [{"message": {"content": "{\"items\": []}"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"items": []}` {
		t.Errorf("payload = %s", payload)
	}
}
