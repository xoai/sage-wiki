# HTTP API (`/v1`)

REST facade over sage-wiki's MCP tool set, for callers that can't speak
MCP: framework pipelines, backend services, serverless, any non-Go
language. Spec: [`api/openapi.yaml`](../../api/openapi.yaml) (OpenAPI 3.1),
kept in sync with the registered routes and the MCP tool registry by a CI
drift check.

> **Experimental, pre-1.0.** Tool semantics can change between releases.
> Pin a version and read the CHANGELOG before upgrading. The older `/api/*`
> routes are internal to the web UI and unstable by design — never build on
> them.

## Running

The facade is mounted on the web server:

```bash
sage-wiki serve --ui --port 3333          # loopback, zero-config
SAGE_WIKI_TOKEN=secret sage-wiki serve --ui --port 3333
```

Loopback needs no token. Binding beyond loopback refuses to start without
one (unchanged from the web UI's rule).

## Auth

When a token is configured, every `/v1/*` route requires it — same
middleware as `/api/*`:

```bash
curl -H "Authorization: Bearer $SAGE_WIKI_TOKEN" http://127.0.0.1:3333/v1/status
# or the query-param form:
curl "http://127.0.0.1:3333/v1/status?token=$SAGE_WIKI_TOKEN"
```

Failures: no/invalid token → `401 unauthenticated`; a Host outside the
allowlist → `403 forbidden`.

## Errors

One envelope for every non-2xx response:

```json
{ "error": { "code": "invalid_argument",
             "message": "depth must be between 1 and 5",
             "details": { "field": "depth", "got": 9 } } }
```

Branch on `code`, never on `message`:

| HTTP | `code` | When |
|---|---|---|
| 400 | `invalid_argument` | Missing/malformed/out-of-range argument |
| 401 | `unauthenticated` | Missing/invalid Bearer token |
| 403 | `forbidden` | Host not allowed; path containment violation |
| 404 | `not_found` | Article does not exist |
| 409 | `conflict` | Reserved (concurrent write conflicts) |
| 412 | `feature_disabled` | `as_of` without `ontology.temporal.enabled`; `mode=global` without `ontology.communities.enabled` |
| 413 | `payload_too_large` | `capture` content over 100 KB |
| 429 | `rate_limited` | Reserved |
| 500 | `internal` | Unclassified tool failure |
| 503 | `unavailable` | Backend/store unavailable |

## Idempotency

All writes accept an `Idempotency-Key` header. A repeated key replays the
stored response verbatim (with an `X-Idempotent-Replay: true` header)
without re-dispatching — use it on every agent-driven retry, especially
`/v1/capture`, which spends LLM budget. Concurrent same-key requests are
deduplicated in flight: one dispatch, all callers get the same response.

**Keys are held in memory: the store is bounded (1000 entries, 24 h TTL)
and does not survive restart.** The key is ignored on GET routes.

## Routes

Every route dispatches to the named MCP tool — no behaviour exists here
that the tool does not provide. Long-running operations (`wiki_compile`,
`wiki_compile_topic`, `wiki_lint`) are not in this version; they arrive as
an async job API.

| Route | MCP tool | Notes |
|---|---|---|
| `GET /v1/search` | `wiki_search` | Response preserves `uncompiled_sources` (compile-on-demand signal) |
| `GET /v1/articles/{path}` | `wiki_read` | `path` is relative to the output dir; traversal → 403; miss → 404 |
| `GET /v1/status` | `wiki_status` | Structured JSON (the tool's prose data, unparsed) |
| `GET /v1/ontology/{entity}/traverse` | `wiki_ontology_query` | `depth` 1–5, `direction` enum, optional `relation` filter |
| `POST /v1/graph/query` | `wiki_graph_query` | `as_of`/`mode=global` gated → 412 `feature_disabled` |
| `GET /v1/entities` | `wiki_list` | `?type=` enum; complete, unpaginated |
| `GET /v1/provenance` | `wiki_provenance` | Exactly one of `source`/`article` |
| `GET /v1/compile/diff` | `wiki_compile_diff` | Read-only |
| `POST /v1/sources` | `wiki_add_source` | Body `{path, type?}`; idempotent with key |
| `PUT /v1/summaries` | `wiki_write_summary` | Upsert keyed by `source` |
| `PUT /v1/articles/{concept}` | `wiki_write_article` | Concept must be lowercase-hyphenated |
| `POST /v1/ontology/entities` | `wiki_add_ontology` | `{id, type?, name?}` — the INT-05 split |
| `POST /v1/ontology/relations` | `wiki_add_ontology` | `{source_id, target_id, relation}` — the other half |
| `POST /v1/learnings` | `wiki_learn` | `type` enum: gotcha, correction, convention, error-fix, api-drift |
| `POST /v1/git/commit` | `wiki_commit` | `{message?}` |
| `POST /v1/capture` | `wiki_capture` | Spends LLM budget; 100 KB cap → 413; key strongly recommended |

## Examples

```bash
SW=http://127.0.0.1:3333
AUTH="Authorization: Bearer $SAGE_WIKI_TOKEN"

curl -s -H "$AUTH" "$SW/v1/status"
curl -s -H "$AUTH" "$SW/v1/search?query=attention&limit=3"
curl -s -H "$AUTH" "$SW/v1/entities?type=concept"
curl -s -H "$AUTH" "$SW/v1/provenance?article=attention"
curl -s -H "$AUTH" -X POST "$SW/v1/graph/query" \
     -H 'Content-Type: application/json' \
     -d '{"question":"how does attention relate to transformers","hops":2}'

# Idempotent write
K=$(uuidgen)
for i in 1 2; do
  curl -s -H "$AUTH" -H "Idempotency-Key: $K" -X POST "$SW/v1/learnings" \
       -H 'Content-Type: application/json' \
       -d '{"type":"gotcha","content":"retries are safe now"}'
done
# second response carries X-Idempotent-Replay: true; exactly one learning stored
```
