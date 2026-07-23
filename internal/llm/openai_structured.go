package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// OpenAI structured outputs (P2-4): response_format json_schema strict
// with a RECURSIVE strict-form derivation (spec §2/i1): required-all +
// additionalProperties:false at every nested object, nullable unions for
// optionals, and a KEY WHITELIST (OpenAI strict 400s on minItems/
// maxItems — the client-side validator keeps enforcing them).
// Degraded mode (schema.Degraded): json_object for the single 400 retry.

var strictAllowedKeys = map[string]bool{
	"type": true, "properties": true, "required": true, "items": true,
	"enum": true, "additionalProperties": true, "description": true,
}

func (p *openaiProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	return func() (*http.Request, error) {
		body := p.formatBody(messages, opts, false)
		if schema.Degraded {
			body["response_format"] = map[string]any{"type": "json_object"}
		} else {
			wire := schema.Schema
			if schema.IsArray {
				wire = schema.Envelope()
			}
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   schema.ToolName(),
					"schema": strictForm(wire),
					"strict": true,
				},
			}
		}
		return p.makeRequest(body)
	}, true, nil
}

// strictForm recursively derives the OpenAI-strict shape: every object
// gets additionalProperties: false and all properties in required;
// draft-07 optional fields become nullable unions; unsupported keys
// (minItems/maxItems) are dropped.
func strictForm(schema map[string]any) map[string]any {
	out := map[string]any{}
	typ, _ := schema["type"].(string)

	for k, v := range schema {
		if !strictAllowedKeys[k] {
			continue
		}
		switch k {
		case "properties":
			props, _ := v.(map[string]any)
			derived := map[string]any{}
			for name, sub := range props {
				subMap, _ := sub.(map[string]any)
				derived[name] = strictForm(subMap)
			}
			out[k] = derived
		case "items":
			if subMap, ok := v.(map[string]any); ok {
				out[k] = strictForm(subMap)
			} else {
				out[k] = v
			}
		case "required":
			// Rebuilt below from properties (required-all).
		default:
			out[k] = v
		}
	}

	if typ == "object" {
		out["additionalProperties"] = false
		props, _ := out["properties"].(map[string]any)
		canonicalReq := toStringSet(schema["required"])
		reqAll := []string{}
		for name := range props {
			reqAll = append(reqAll, name)
			// Optional in the canonical schema → nullable union.
			if !canonicalReq[name] {
				sub, _ := props[name].(map[string]any)
				if sub == nil {
					continue // malformed schema property — skip, don't panic
				}
				subType, _ := sub["type"].(string)
				if subType != "" {
					sub["type"] = []string{subType, "null"}
					// Nullable-union enum members get null appended (OpenAI
					// strict rejects a null value absent from enum).
					if enumVals, hasEnum := sub["enum"].([]any); hasEnum {
						sub["enum"] = append(enumVals, nil)
					}
				}
			}
		}
		sort.Strings(reqAll) // deterministic wire output
		out["required"] = reqAll
	}
	return out
}

func toStringSet(v any) map[string]bool {
	set := map[string]bool{}
	switch list := v.(type) {
	case []string:
		for _, s := range list {
			set[s] = true
		}
	case []any:
		for _, s := range list {
			if str, ok := s.(string); ok {
				set[str] = true
			}
		}
	}
	return set
}

func (p *openaiProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("openai structured: no choices in response")
	}
	return json.RawMessage(result.Choices[0].Message.Content), nil
}
