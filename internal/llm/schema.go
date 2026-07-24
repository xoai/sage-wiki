package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONSchema is a call site's canonical JSON Schema (draft-07 subset).
// Array-shaped sites author the ARRAY schema (IsArray: true); the native
// mechanisms require an object root, so the wire format wraps it in an
// envelope (design D2). Canonical schemas use draft-07 optional fields;
// per-provider derived forms handle strictness (design D4/D6).
type JSONSchema struct {
	Name        string
	Description string
	Schema      map[string]any
	IsArray     bool
	// Degraded is set by the client for the single json_object degrade
	// retry (OpenAI only) — the formatter emits the degraded mechanism.
	Degraded bool
}

// Envelope returns the object-rooted wrapper the native mechanisms
// require. Object-shaped sites get Schema unchanged.
func (s JSONSchema) Envelope() map[string]any {
	if !s.IsArray {
		return s.Schema
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"items": s.Schema},
		"required":             []string{"items"},
		"additionalProperties": false,
	}
}

// ToolName returns the sanitized anthropic tool name (design rule:
// ^[a-zA-Z0-9_-]{1,64}$, default "json_response").
func (s JSONSchema) ToolName() string {
	name := s.Name
	if name == "" {
		return "json_response"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		return "json_response"
	}
	return out
}

// ValidateJSON validates payload against a draft-07 subset schema:
// type, required, properties (recursive), items, additionalProperties
// (wire-level only, not validated), enum, minItems/maxItems. Missing-optional and
// null-optional are treated identically (design D4: per-provider
// output-shape divergence absorbed).
func ValidateJSON(schema map[string]any, payload json.RawMessage) error {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return fmt.Errorf("structured: unparseable payload: %w", err)
	}
	return validateValue(schema, v, "$")
}

func validateValue(schema map[string]any, v any, path string) error {
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("structured: %s: expected object, got %T", path, v)
		}
		if req, ok := schema["required"].([]string); ok {
			for _, k := range req {
				if _, present := obj[k]; !present {
					return fmt.Errorf("structured: %s: missing required field %q", path, k)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		canonicalReq := map[string]bool{}
		if req, ok := schema["required"].([]string); ok {
			for _, k := range req {
				canonicalReq[k] = true
			}
		}
		for k, sub := range props {
			subSchema, _ := sub.(map[string]any)
			val, present := obj[k]
			if !present {
				continue
			}
			// Required-null hole (Gate 3 i1): a required field present as
			// null fails unless the property is a nullable union.
			if val == nil {
				if canonicalReq[k] && !isNullableUnion(subSchema) {
					return fmt.Errorf("structured: %s: required field %q is null", path, k)
				}
				continue
			}
			if err := validateValue(subSchema, val, path+"."+k); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("structured: %s: expected array, got %T", path, v)
		}
		if min, ok := schema["minItems"].(int); ok && len(arr) < min {
			return fmt.Errorf("structured: %s: %d items < minItems %d", path, len(arr), min)
		}
		if minF, ok := schema["minItems"].(float64); ok && len(arr) < int(minF) {
			return fmt.Errorf("structured: %s: %d items < minItems %d", path, len(arr), int(minF))
		}
		if max, ok := schema["maxItems"].(int); ok && len(arr) > max {
			return fmt.Errorf("structured: %s: %d items > maxItems %d", path, len(arr), max)
		}
		if maxF, ok := schema["maxItems"].(float64); ok && len(arr) > int(maxF) {
			return fmt.Errorf("structured: %s: %d items > maxItems %d", path, len(arr), int(maxF))
		}
		items, _ := schema["items"].(map[string]any)
		for i, el := range arr {
			if err := validateValue(items, el, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("structured: %s: expected string, got %T", path, v)
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("structured: %s: expected integer, got %v", path, v)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("structured: %s: expected number, got %T", path, v)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("structured: %s: expected boolean, got %T", path, v)
		}
	}
	if enumVals, ok := enumList(schema["enum"]); ok {
		found := false
		for _, e := range enumVals {
			if safeEqual(e, v) || numericEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("structured: %s: %v not in enum", path, v)
		}
	}
	return nil
}

// enumList normalizes Go-authored enum forms ([]any, []string, []int).
func enumList(v any) ([]any, bool) {
	switch list := v.(type) {
	case []any:
		return list, true
	case []string:
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out, true
	case []int:
		out := make([]any, len(list))
		for i, n := range list {
			out[i] = n
		}
		return out, true
	}
	return nil, false
}

// safeEqual compares without panicking on uncomparable types (slice/map).
func safeEqual(a, b any) (eq bool) {
	defer func() {
		if recover() != nil {
			eq = false
		}
	}()
	return a == b
}

// numericEqual compares numeric enum values across int/float64 domains
// (Go-authored ints vs JSON-decoded float64s).
func numericEqual(a, b any) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	return aok && bok && af == bf
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// isNullableUnion reports whether a strict-derived property allows null
// (["<type>", "null"]).
func isNullableUnion(schema map[string]any) bool {
	types, ok := schema["type"].([]string)
	if !ok {
		if tAny, isArr := schema["type"].([]any); isArr {
			for _, t := range tAny {
				if t == "null" {
					return true
				}
			}
		}
		return false
	}
	for _, t := range types {
		if t == "null" {
			return true
		}
	}
	return false
}

// ParseJSONFromText is the shared fence-strip+bracket-hunt parser
// (extracted from the duplicated site parsers; tools_write.go and
// grounding.go keep their own no-bracket-hunt parsers per spec §4).
// Root rule: the opening brace is the FIRST JSON structural char in the
// text ('[' or '{', whichever comes first); the close is the matching
// LAST char of the same kind.
func ParseJSONFromText(text string) (json.RawMessage, error) {
	text = strings.TrimSpace(text)

	// Strip markdown code fences if present.
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		text = strings.Join(jsonLines, "\n")
	}

	startArr := strings.Index(text, "[")
	startObj := strings.Index(text, "{")
	var start, end int
	var closer byte
	switch {
	case startArr >= 0 && (startObj < 0 || startArr < startObj):
		start, closer = startArr, ']'
	case startObj >= 0:
		start, closer = startObj, '}'
	default:
		return nil, fmt.Errorf("structured: no JSON found in text")
	}
	end = strings.LastIndex(text, string(closer))
	if end <= start {
		return nil, fmt.Errorf("structured: unbalanced JSON in text")
	}
	return json.RawMessage(text[start : end+1]), nil
}

// ErrStructuredUnsupported is returned by ParseStructuredResponse on
// providers without a mechanism (stub implementations — the fallback
// path covers them in practice).
var ErrStructuredUnsupported = fmt.Errorf("structured outputs not supported by this provider")

// StatusError carries the HTTP status for the degrade trigger (spec §3).
// Error() matches the pre-existing plain-error format byte-for-byte.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("llm: API returned %d: %s", e.Code, e.Body)
}
