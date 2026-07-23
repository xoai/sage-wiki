# Design: P2-4 — Provider-native structured outputs

**Status:** draft (first commit of PR per Phase-2 spec preamble)
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-4
**Cycle:** `.sage/work/20260723-p2-4-structured-outputs/`

## 1. Problem

Five call sites parse LLM JSON by fence-stripping + brace-hunting +
`json.Unmarshal` (verified inventory):

| Site | Shape | Current parser |
|---|---|---|
| `internal/trust/grounding.go:45` | claims array | direct Unmarshal of raw content |
| `internal/compiler/concepts.go:338` | concepts array | fence-strip (:313) + bracket hunt |
| `internal/mcp/tools_write.go:486-491` | capture items | fence-strip + Unmarshal |
| `internal/search/expand.go:128` | expansion object | bracket hunt + Unmarshal |
| `internal/search/rerank.go:155` | rerank entries | bracket hunt + Unmarshal |

A chatty model (prose around JSON, nested fences, trailing commentary)
breaks every one of these silently. Provider-native JSON constraints
remove the failure class; openai-compatible keeps the fallback.

## 2. Design decisions

### D1 — One method on the client, mechanism chosen inside

```go
type JSONSchema struct {
    Name        string
    Description string
    Schema      map[string]any // JSON Schema draft-07 subset
}

func (c *Client) StructuredCompletion(ctx context.Context, messages []Message, schema JSONSchema, opts CallOpts) (json.RawMessage, error)
```

The method asks the PROVIDER for its mechanism via a new interface (D2),
validates the result against the schema subset (D4), and on mechanism
error (not content error) falls back to fence-strip+validate (D5). Call
sites swap `parseFooJSON(resp.Content)` for `StructuredCompletion(...)`
and never branch.

### D2 — Per-provider mechanism via a new provider interface

Memory rule applied (extraParams openai-only bug): the wiring uses an
INTERFACE so it reaches nonBatchProvider wrappers + anthropic, never a
concrete-type assertion.

```go
type structuredMechanism interface {
    // SupportsStructured reports the mechanism available, or "" for fallback.
    SupportsStructured() string // "tool_use" | "response_format" | "response_schema" | ""
    // FormatStructuredRequest injects the constraint into the request body.
    FormatStructuredRequest(body map[string]any, schema JSONSchema)
    // ExtractStructured pulls the constrained JSON from the response.
    ExtractStructured(resp *Response) (json.RawMessage, error)
}
```

- **anthropic**: tool_use — a synthetic tool named after the schema
  (`Name`) with `input_schema = schema.Schema`, `tool_choice = {type:
  "tool", name}`. Extraction: the tool_use content block's `input`.
- **openai**: `response_format = {type: "json_schema", json_schema:
  {name, schema, strict: true}}`. Degraded (D5): `json_object` mode when
  the backend 400s on json_schema.
- **gemini**: `generationConfig.responseSchema = schema.Schema` +
  `responseMimeType = "application/json"`. Note: Gemini's responseSchema
  is OpenAPI-subset, not draft-07 — type mapping handled in the gemini
  formatter (D6).
- **openai-compatible / qwen / ollama / nonBatch wrappers**: `""` →
  fallback. Wrappers FORWARD the interface to their inner provider
  (nonbatch.go) — the wrapper itself reports "".

### D3 — No prompt mutation

The constraint mechanism is request-body only. System/user prompts are
untouched (existing prompt text already says "respond with JSON"; the
mechanism enforces it). Anthropic forced tool choice leaves no room for
prose; OpenAI json_schema strict ditto; Gemini responseMimeType ditto.

### D4 — Validation: subset validator, no new dependency

`internal/llm/schema.go`: validates the parsed JSON against the subset
the schemas use — `type` (object|array|string|number|integer|boolean),
`required`, `properties` (recursive), `items`, `additionalProperties:
false` presence-only, `enum`. Anything beyond the subset is ignored by
the validator (schemas are authored within the subset). A validation
failure is a CONTENT error: returned to the caller as-is (the model
failed the schema; retrying the same call won't help — callers treat it
like today's parse failure).

### D5 — Fallback semantics (mechanism vs content errors, exactly)

- **Mechanism unavailable** (SupportsStructured returns ""): use the
  existing fence-strip+parse of `resp.Content` (the site's current
  parser, extracted into a shared `ParseJSONFromText` helper) → validate.
- **Mechanism request rejected** (HTTP 400 mentioning the constraint
  field): OpenAI degrades json_schema → json_object + retry once;
  other providers → fence-strip fallback of the retried-plain response.
  One retry max, logged.
- **Mechanism succeeded but content invalid**: validation error
  returned (no fallback — the constraint worked, the content is wrong;
  falling back would mask real model failures).

### D6 — Schema representation per provider family

One canonical JSON Schema per call site (authored in each call site's
package as the single source of truth). The gemini formatter maps
draft-07 subset → OpenAPI subset (nullable unions → `nullable: true`,
`additionalProperties` dropped, `required` kept, no `$ref` in our
schemas). The anthropic/openai formatters pass the schema through
unchanged (both accept draft-07 subset).

### D7 — Prompt caching preserved

Anthropic: the synthetic tool definition is appended AFTER the cached
prefix (tools array is part of the request, tool defs go in `tools` —
the cached system/messages prefix is unchanged; anthropic prompt caching
keys on the prefix, tools can be cached too but are small here). OpenAI/
Gemini: response_format/generationConfig are not part of cached content.
No cache-invalidation risk (verified by the request-shape tests).

### D8 — The five call-site schemas

Authored per site (source of truth in the site's package):
- grounding.go: array of `{claim, source_chunk_ids[]}` (shape read from
  existing code at implementation time and pinned in spec).
- concepts.go: array of `{name, type, definition, source_quote?}`.
- tools_write.go capture: array of `{title?, content, tags[]?}` (shape
  pinned from current parser).
- expand.go: `{queries[], hyde?}` (from ExpandedQuery).
- rerank.go: array of `{id, score}` (from rerank entries).

## 3. Non-goals

Full JSON-Schema engine, structured batch path, streaming structured
mode, new providers, prompt rewrites, changing the MCP capture schema
(the capture tool's external schema is untouched — internal parsing
only).

## 4. Test strategy

- Per-provider request-shape tests (httptest): anthropic body has
  tool_use+tool_choice+input_schema; openai has response_format
  json_schema; gemini has responseSchema+mimeType; wrapper forwards or
  reports "".
- Extraction tests per provider (recorded response fixtures).
- Validator tests: required missing, wrong type, nested arrays, enum
  violation, additionalProperties.
- The spec-mandated chatty-mock test: response with prose around JSON
  that breaks fence-stripping, structured path extracts cleanly.
- Fallback test: openai-compatible mock → fence-strip path, byte-
  identical results to the old parser.
- Mechanism-rejected retry test (openai 400 on json_schema → json_object
  retry, once).
- Full suite + `CGO_ENABLED=0 go build ./...` green.
