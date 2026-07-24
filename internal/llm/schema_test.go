package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeShape(t *testing.T) {
	s := JSONSchema{
		Name: "test",
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
				},
				"required": []string{"text"},
			},
		},
		IsArray: true,
	}
	env := s.Envelope()
	if env["type"] != "object" {
		t.Errorf("envelope type = %v", env["type"])
	}
	props := env["properties"].(map[string]any)
	if props["items"] == nil {
		t.Error("envelope missing items property")
	}
	req := env["required"].([]string)
	if len(req) != 1 || req[0] != "items" {
		t.Errorf("envelope required = %v", req)
	}
	if env["additionalProperties"] != false {
		t.Error("envelope must set additionalProperties: false")
	}

	obj := JSONSchema{Name: "x", Schema: map[string]any{"type": "object"}}
	if got := obj.Envelope(); got["type"] != "object" {
		t.Error("object schema passes through unchanged")
	}
}

func TestToolNameSanitization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "json_response"},
		{"valid-name_1", "valid-name_1"},
		{"has space", "has_space"},
		{"has/slash", "has_slash"},
		{"weird@#$chars", "weird___chars"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		if got := (JSONSchema{Name: tc.in}).ToolName(); got != tc.want {
			t.Errorf("ToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateJSONBasics(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
		},
		"required": []string{"name"},
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"name":"x","count":3}`)); err != nil {
		t.Errorf("valid: %v", err)
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"count":3}`)); err == nil {
		t.Error("missing required not caught")
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"name":42}`)); err == nil {
		t.Error("wrong type not caught")
	}
}

func TestValidateJSONArrayMinMax(t *testing.T) {
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "integer"},
				"score": map[string]any{"type": "number"},
			},
			"required": []string{"id", "score"},
		},
		"minItems": 1,
	}
	if err := ValidateJSON(schema, json.RawMessage(`[{"id":1,"score":0.5}]`)); err != nil {
		t.Errorf("valid: %v", err)
	}
	if err := ValidateJSON(schema, json.RawMessage(`[]`)); err == nil {
		t.Error("minItems: 1 not enforced on empty array")
	}
}

func TestValidateJSONNullOptional(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"aliases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"name"},
	}
	// missing optional OK, null optional OK (nullable union from strict derivation)
	if err := ValidateJSON(schema, json.RawMessage(`{"name":"x"}`)); err != nil {
		t.Errorf("missing optional: %v", err)
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"name":"x","aliases":null}`)); err != nil {
		t.Errorf("null optional: %v", err)
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"name":"x","aliases":["a"]}`)); err != nil {
		t.Errorf("present optional: %v", err)
	}
}

func TestParseJSONFromText(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"bare array", `[{"a":1}]`, `[{"a":1}]`, false},
		{"fenced", "```json\n[{\"a\":1}]\n```", `[{"a":1}]`, false},
		{"prose around", "Here is the JSON:\n[{\"a\":1}]\nDone.", `[{"a":1}]`, false},
		{"object root", `{"lex":["a"]}`, `{"lex":["a"]}`, false},
		{"fenced object", "```\n{\"lex\":[\"a\"]}\n```", `{"lex":["a"]}`, false},
		{"garbage", "no json here", "", true},
	}
	for _, tc := range cases {
		got, err := ParseJSONFromText(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %s", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestStatusErrorFormat(t *testing.T) {
	err := &StatusError{Code: 400, Body: "bad request"}
	if err.Error() != "llm: API returned 400: bad request" {
		t.Errorf("format = %q", err.Error())
	}
}

func TestValidateJSONRequiredNull(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{"type": "string"},
		},
		"required": []string{"text"},
	}
	if err := ValidateJSON(schema, json.RawMessage(`{"text": null}`)); err == nil {
		t.Error("required-null must fail validation")
	}
	// Nullable union: required-null is ACCEPTED.
	nullable := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"aliases": map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}},
		},
		"required": []string{"aliases"},
	}
	if err := ValidateJSON(nullable, json.RawMessage(`{"aliases": null}`)); err != nil {
		t.Errorf("nullable-union required-null must pass: %v", err)
	}
}

func TestValidateJSONFloatMinMax(t *testing.T) {
	schema := map[string]any{
		"type": "array", "items": map[string]any{"type": "string"},
		"minItems": float64(1), "maxItems": float64(2),
	}
	if err := ValidateJSON(schema, json.RawMessage(`[]`)); err == nil {
		t.Error("float64 minItems not enforced")
	}
	if err := ValidateJSON(schema, json.RawMessage(`["a","b","c"]`)); err == nil {
		t.Error("float64 maxItems not enforced")
	}
	if err := ValidateJSON(schema, json.RawMessage(`["a","b"]`)); err != nil {
		t.Errorf("valid: %v", err)
	}
}

func TestValidateJSONEnumNumeric(t *testing.T) {
	schema := map[string]any{
		"type": "integer",
		"enum": []int{1, 2, 3},
	}
	if err := ValidateJSON(schema, json.RawMessage(`2`)); err != nil {
		t.Errorf("int enum should match JSON float64: %v", err)
	}
	if err := ValidateJSON(schema, json.RawMessage(`5`)); err == nil {
		t.Error("non-member not rejected")
	}
}

func TestValidateJSONEnumUncomparableSafe(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{"type": "array"},
		},
		"enum": []any{[]any{"a"}},
	}
	// must not panic on uncomparable enum member comparison
	_ = ValidateJSON(schema, json.RawMessage(`{"tags": ["a"]}`))
}
