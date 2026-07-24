package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Gemini structured outputs (P2-4): generationConfig.responseSchema +
// responseMimeType application/json, with the draft-07 → OpenAPI subset
// mapping (design D6): optional fields → nullable: true,
// additionalProperties dropped, minItems/maxItems kept (OpenAPI supports
// them).

func (p *geminiProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	return func() (*http.Request, error) {
		body, model := p.formatBody(messages, opts) // model is defaulted inside formatBody
		wire := schema.Schema
		if schema.IsArray {
			wire = schema.Envelope()
		}
		config := map[string]any{
			"responseMimeType": "application/json",
			"responseSchema":   openAPIForm(wire),
		}
		if existing, ok := body["generationConfig"].(map[string]any); ok {
			for k, v := range existing {
				config[k] = v
			}
		}
		body["generationConfig"] = config
		url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, model, p.apiKey)
		req, err := http.NewRequest("POST", url, jsonBody(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, true, nil
}

// openAPIForm maps the draft-07 subset to Gemini's OpenAPI subset:
// optional fields → nullable: true; additionalProperties dropped;
// type/required/properties/items/enum/minItems/maxItems preserved.
func openAPIForm(schema map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range schema {
		switch k {
		case "additionalProperties":
			continue // OpenAPI subset has no additionalProperties
		case "properties":
			props, _ := v.(map[string]any)
			derived := map[string]any{}
			for name, sub := range props {
				subMap, _ := sub.(map[string]any)
				derived[name] = openAPIForm(subMap)
			}
			out[k] = derived
		case "items":
			if subMap, ok := v.(map[string]any); ok {
				out[k] = openAPIForm(subMap)
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	if props, ok := out["properties"].(map[string]any); ok {
		canonicalReq := toStringSet(schema["required"])
		for name := range props {
			if !canonicalReq[name] {
				sub, _ := props[name].(map[string]any)
				if sub == nil {
					continue // malformed schema property — skip, don't panic
				}
				sub["nullable"] = true
			}
		}
	}
	return out
}

func (p *geminiProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	for _, c := range result.Candidates {
		for _, part := range c.Content.Parts {
			if part.Text != "" {
				return json.RawMessage(part.Text), nil
			}
		}
	}
	return nil, fmt.Errorf("gemini structured: no text part in response")
}
