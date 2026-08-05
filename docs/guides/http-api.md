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

The `/v1` facade is mounted on **both** serve stacks (same route table,
`internal/api/router.go`):

- **Serve mode** (`sage-wiki serve --addr 127.0.0.1:8484`, SPEC-02) —
  REST + MCP at `/mcp` + `/events/stream`; **bearer-only** auth (no
  Host/Origin checks). See [Serve mode](serve-mode.md).
- **Web UI** (`sage-wiki serve --ui --port 3333`) — bearer auth **plus**
  a Host allowlist (DNS-rebind guard) and an `Origin` header check
  (CSRF guard) on state-changing routes.

```bash
sage-wiki serve --addr 127.0.0.1:8484        # SPEC-02 REST + /mcp (bearer-only)
sage-wiki serve --ui --port 3333             # web UI (bearer + Host + Origin)
SAGE_WIKI_TOKEN=secret sage-wiki serve --addr 0.0.0.0:8484
```

Loopback needs no token on either stack. Binding beyond loopback refuses
to start without one.

## Auth

When a token is configured, every `/v1/*` route requires it:

```bash
curl -H "Authorization: Bearer $SAGE_WIKI_TOKEN" http://127.0.0.1:8484/v1/status
# or the query-param form:
curl "http://127.0.0.1:8484/v1/status?token=$SAGE_WIKI_TOKEN"
```

Failures: no/invalid token → `401 unauthenticated`. **On the web UI stack
only** (`serve --ui`): a Host outside the allowlist → `403 forbidden`, and
state-changing requests (POST/PUT/DELETE/PATCH on `/v1/*`) whose `Origin`
header does not match the Host are also refused 403 — non-browser clients
should simply not send an `Origin` header. The SPEC-02 `serve --addr`
stack enforces bearer only (it has no Host/Origin guards).

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

500 messages from file-writing tools (`wiki_read`, sources, summaries,
articles, capture, git/commit) are deliberately generic — tool error text
can contain absolute server paths, so it goes to the server log instead of
the response. This also suppresses some path-free actionable messages
(e.g. git identity misconfiguration); correlate a 500 from these tools
with the server log, which carries the full text.

## Idempotency

All writes accept an `Idempotency-Key` header. A repeated key replays the
stored response verbatim (with an `X-Idempotent-Replay: true` header)
without re-dispatching — use it on every agent-driven retry, especially
`/v1/capture`, which spends LLM budget. Concurrent same-key requests are
deduplicated in flight: one dispatch, all callers get the same response.

**Keys are held in memory: the store is bounded (1000 entries, 24 h TTL)
and does not survive restart.** The key is ignored on GET routes. Keys are
global, not scoped per endpoint — reuse a key only for a retry of the same
request; a fresh operation needs a fresh key. JSON request bodies are
capped at 1 MiB (`413 payload_too_large` beyond; the separate 100 KB cap
applies to `/v1/capture` content). Unknown paths answer `404 not_found`;
a known path under the wrong method answers 405 with an `Allow` header.

## Routes

Every route dispatches to the named MCP tool — no behaviour exists here
that the tool does not provide. Long-running operations (`wiki_compile`,
`wiki_compile_topic`, `wiki_lint`) run as async jobs — see
[Async jobs](#async-jobs) below.

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

## Async jobs

Compile and lint run for minutes, so they are job submissions, not blocking
calls. Submit, poll, optionally cancel:

```bash
# Submit a full compile (any compile flag present selects full mode).
curl -s -X POST $SW/v1/jobs/compile -d '{"dry_run": false}'
# → 202 Accepted, Location: /v1/jobs/<job_id>
#   {"job_id":"…","kind":"compile","status":"pending",…}

# Topic compile (compile-on-demand) — mutually exclusive with compile flags.
curl -s -X POST $SW/v1/jobs/compile -d '{"topic": "quantum computing", "max_sources": 20}'

# Lint.
curl -s -X POST $SW/v1/jobs/lint -d '{"pass": "connections", "fix": false}'

# Poll.
curl -s $SW/v1/jobs/<job_id>
# status: pending → running → done | failed | cancelled

# List (bounded to 100 most recent, FIFO eviction) and filter.
curl -s "$SW/v1/jobs?status=running"

# Cancel (best-effort: the checkpoint stays resumable).
curl -s -X DELETE $SW/v1/jobs/<job_id>
```

Semantics that matter:

- **Concurrency:** submitting a compile (either mode) while one is active
  returns `409 conflict` with `details.active_job_id` — poll that job
  instead. Lint jobs never block compiles and vice versa.
- **Idempotency:** send `Idempotency-Key` on submit; a replayed key returns
  the same `job_id` with `X-Idempotent-Replay: true` and does not dispatch
  again (prevents duplicate LLM spend on client retry). Keys are scoped
  per kind.
- **Progress:** compile jobs mirror the compile progress hub into the job's
  `progress` field; lint jobs report `{"stage": "running"|"done"}`.
- **Retention:** job records are in-memory and process-scoped (same
  restart semantics as the idempotency store) — a restart loses the list,
  and the next compile resumes from the checkpoint.
- **Errors:** a failed job carries the standard error envelope in its
  `error` field; paths are scrubbed from messages.

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
