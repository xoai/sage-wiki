# Design: P2-4 — Provider-native structured outputs

**Status:** draft, review iteration 2 (first commit of PR per Phase-2 spec preamble)

> Iteration log: i1 found 3C/6M/5S/1cos — interface doesn't compose with the
> provider split (no body-map seam), anthropic ParseResponse discards
> tool_use blocks, root-array schemas incompatible with both mechanisms,
> wrong pinned shapes, OpenAI strict-mode rules, wrapper contradiction,
> untyped degrade trigger, cache-prefix breakage. All redesigned below.
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

### D2 — Mechanism as a Provider-interface method (i1 redesign)

The i1 interface could not compose: bodies are built privately in each
provider's formatBody and never exposed as maps, and anthropic's
ParseResponse discards tool_use blocks. The mechanism therefore lives
INSIDE each provider via one new method on the Provider interface
(memory rule: interface-based cross-cutting wiring, never concrete-type
assertions — the extraParams bug pattern):

```go
type Provider interface {
    // ... existing methods ...
    // StructuredCompletion issues messages with the provider's native
    // JSON constraint and returns the parsed structured payload.
    // ok == false means the provider has no mechanism — caller falls back
    // to fence-strip+parse of a plain completion.
    StructuredCompletion(ctx context.Context, messages []Message, schema JSONSchema, opts CallOpts) (payload json.RawMessage, ok bool, err error)
}
```

- **anthropic**: `tools: [{name, description, input_schema}]` +
  `tool_choice: {type: "tool", name}` in its own formatBody variant.
  Extraction REQUIRES extending anthropic's ParseResponse to also collect
  `tool_use` blocks into `Response.ToolCalls` (new field — today it keeps
  only text/thinking and discards tool_use entirely, i1-CRITICAL-2).
- **openai**: `response_format = {type: "json_schema", json_schema:
  {name, schema, strict: true}}` in its formatBody variant; degrade path
  in D5.
- **gemini**: `generationConfig.responseSchema` + `responseMimeType:
  "application/json"` with the draft-07→OpenAPI mapping (D6).
- **openai-compatible / qwen / ollama / ALL nonBatch wrappers**: return
  `ok == false` UNCONDITIONALLY — even a wrapper around a raw openai
  provider. Pinned decision (i1 contradiction resolved): spec requires
  byte-identical fallback for openai-compatible, and wrappers cannot
  guarantee the inner backend honors the constraint; predictability over
  opportunistic structure.
- **Envelope wrapping (i1-CRITICAL-3):** 4 of 5 sites are root ARRAYS,
  and both anthropic input_schema and OpenAI strict mode require an
  object root. Every array-shaped site wraps: schema is `{type: object,
  properties: {items: <array schema>}, required: ["items"],
  additionalProperties: false}`; StructuredCompletion unwraps `items`
  before returning. Object-shaped sites (expand) use their schema
  directly.

### D3 — No prompt mutation

The constraint mechanism is request-body only. System/user prompts are
untouched (existing prompt text already says "respond with JSON"; the
mechanism enforces it). Anthropic forced tool choice leaves no room for
prose; OpenAI json_schema strict ditto; Gemini responseMimeType ditto.

### D4 — Validation: subset validator, no new dependency

`internal/llm/schema.go`: validates the parsed JSON against the subset
the schemas use — `type` (object|array|string|number|integer|boolean),
`required`, `properties` (recursive), `items`, `additionalProperties:
false` presence-only, `enum`, **`minItems`/`maxItems`** (added i1 —
rerank's silent-empty-entries failure mode is undetectable without it).
Anything beyond the subset is ignored. A validation failure is a CONTENT
error returned to the caller (mapped to each site's existing graceful
degrade, D5). **OpenAI strict-mode compliance (i1):** every object in
every authored schema sets `additionalProperties: false` AND lists all
properties in `required` EXCEPT envelope-optional fields — strict mode
400s otherwise.

### D5 — Fallback semantics (mechanism vs content errors, exactly)

- **Mechanism unavailable** (`ok == false`): plain completion → the
  shared fence-strip parser (the site's current logic, extracted to
  `internal/llm.ParseJSONFromText`) → validate. Byte-identical to today
  for openai-compatible.
- **OpenAI json_schema rejected (400):** the client needs the status —
  chatCompletionDirect currently wraps non-retryable statuses in a plain
  fmt.Errorf. Pinned change: a typed `StatusError{Code, Body}` (matching
  RateLimitError's pattern) so the structured path degrades json_schema →
  json_object ONCE, only when Code==400 AND Body mentions
  "response_format"/"json_schema" (a context-length 400 never degrades).
  If the json_object retry also 400s, plain-completion + fence-strip.
- **Mechanism succeeded but content invalid**: validation error
  returned (no fallback — the constraint worked, the content is wrong).
- **Graceful-degrade parity:** expand.go and rerank.go currently
  degrade gracefully on parse failure (fallback expansion / unranked
  order). Call sites keep their existing error handling: a
  StructuredCompletion error maps to the SAME degrade path as today's
  parse error — no behavior change on failure, only on success quality.

### D6 — Schema representation per provider family

One canonical JSON Schema per call site (authored in each call site's
package as the single source of truth). The gemini formatter maps
draft-07 subset → OpenAPI subset (nullable unions → `nullable: true`,
`additionalProperties` dropped, `required` kept, no `$ref` in our
schemas). The anthropic/openai formatters pass the schema through
unchanged (both accept draft-07 subset).

### D7 — Structured path is deliberately NON-cached (i1 correction)

Anthropic's cache prefix is evaluated tools → system → messages, so
inserting a per-site tool definition BEFORE the cached system block
would change the prefix and break the existing system cache_control
breakpoint (anthropic.go:152-156). Pinned: StructuredCompletion routes
through the DIRECT (non-cached) path — structured calls are small
(extraction/claims/expansion), cache benefit is minimal, and correctness
wins. Cached structured mode (moving tool defs into the cached prefix)
is documented as future work, not assumed. OpenAI/Gemini have no
server-side cache in this codebase — unaffected.

### D8 — The five call-site schemas (verified against code)

Authored per site (source of truth in the site's package); array shapes
use the D2 envelope:
- grounding.go (claims): `items: [{text}]` — Claim is a single-field
  object (grounding.go:12). minItems: 1 (empty claims are a model
  failure, not data).
- concepts.go: `items: [{name, aliases?, sources[], type}]` — required:
  name, sources, type (concepts.go:19-24).
- tools_write.go capture: `items: [{title, content}]` — both required
  (tools_write.go:447-450).
- expand.go (object root, no envelope): `{lex[], vec[], hyde}` — all
  required (expand.go:92-96).
- rerank.go: `items: [{id, score}]` — id integer, score number,
  minItems: 1 (rerank.go:120-123; an empty entries list is the model's
  silent-failure mode the validator must catch — D4 adds minItems).

## 3. Non-goals

Full JSON-Schema engine, structured batch path, streaming structured
mode, new providers, prompt rewrites, changing the MCP capture schema
(the capture tool's external schema is untouched — internal parsing
only).

## 4. Test strategy

- Per-provider request-shape tests (httptest): anthropic body has
  tool_use+tool_choice+input_schema (envelope object root); openai has
  response_format json_schema with strict:true +
  additionalProperties:false; gemini has responseSchema+mimeType;
  wrappers return ok==false even wrapping openai.
- Root-array envelope test (array sites 400 without it).
- Cached+structured NON-interaction test: structured path does not
  touch the cached request builder.
- Context-length 400 does NOT trigger the json_object degrade.
- Strict-mode compliance test: authored schemas validate against the
  strict-mode rules (required + additionalProperties:false).
- Tool-name sanitization: schema Name sanitized to
  `^[a-zA-Z0-9_-]{1,64}$` (default `json_response`).
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
