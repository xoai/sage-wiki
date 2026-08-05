# Serve Mode

`sage-wiki serve` runs the engine as a persistent process: REST API,
MCP tools over streamable HTTP, async compile jobs, and metrics on one
listener — so agents and UIs connect to a warm engine instead of paying
process startup per invocation.

## Modes

- **HTTP mode (default)**: `serve --addr 127.0.0.1:8484` (or bare
  `serve`). Takes the **workspace lock** — CLI commands that mutate
  (compile/capture/query/ingest) fail fast with "workspace is locked"
  while a workspace is served (the single-writer invariant). Read-only
  paths keep working.
- **MCP stdio/SSE**: `serve --transport stdio|sse` (pre-existing,
  lock-free).
- **Web UI**: `serve --ui` (pre-existing, lock-free).

NOTE (behavior change): bare `serve` previously started the stdio MCP
server. Pass `--transport stdio` explicitly for the old behavior.

## Flags

| Flag | Default | Purpose |
|---|---|---|
| `--addr` | `127.0.0.1:8484` (bare serve) | HTTP bind |
| `--workspace` | `--project` | workspace dir |
| `--token-file` | — | bearer tokens, one per line (`#` comments ok) |
| `--max-concurrent-compiles` | 2 | global compile cap |
| `--drain-timeout` | 30s | shutdown drain budget (min 10s, warns when clamped) |
| `--insecure-no-auth` | false | allow non-loopback bind without tokens |

A non-loopback `--addr` without any token is refused with a clear error.
Tokens resolve: `--token` flag > `--token-file` > `SAGE_WIKI_TOKEN` >
config. The server warns when the token file is group/world-readable;
tokens never appear in logs. TLS is out of scope — deploy behind a
reverse proxy or tunnel. **Caveat:** bearer tokens are also accepted via
the `?token=` query parameter (web-server precedent) — URLs leak via
proxy/browser logs, so prefer the `Authorization: Bearer` header and
expect proxies to log query strings.

## REST surface

`GET /healthz`, `GET /readyz` (flips after workspace open),
`POST /capture`, `POST /search`, `POST /compile` → `{job_id}`,
`GET /jobs`, `GET /jobs/{id}`, `GET /graph/query?q=&mode=&as_of=`,
`GET /docs/{id}` (article DocIDs only), `GET /export` (tar stream),
`GET /metrics`, `GET /events/stream` (SSE), and `/mcp` (all 19 tools over
streamable HTTP). `/v1/*` stays as-is.

> **Two MCP-over-network paths.** This stack exposes MCP at **`/mcp`**
> (streamable HTTP, SPEC-02). The **web UI** stack (`serve --ui`) exposes
> MCP at **`/sse`** (SSE transport) instead — see
> [Self-hosted server](self-hosted-server.md). Both serve the same 19
> tools; pick the stack that fits your client. `/v1/` is hosted by both
> (see [HTTP API](http-api.md)).

## Event surfaces (SPEC-07)

The workspace runs an event bus (see [events config](configuration.md#events)):

- **Audit trail** — every event also lands as JSONL under `events/`
  (rotating generations), CLI and serve alike.
- **`GET /events/stream`** — Server-Sent Events, token-gated like every
  non-health route: one `data:` line per event, `: keepalive` comment
  every 15s, per-workspace by construction (multi-workspace mode serves
  each workspace's stream under `/w/{name}/events/stream`).
- **Webhooks** — signed at-least-once delivery to your endpoints; see
  [webhooks](../webhooks.md).

## Async compile jobs

`POST /compile` returns 202 + `job_id`. Jobs run FIFO per workspace
(the single-writer invariant — one compile per workspace at a time, so
`--max-concurrent-compiles` only becomes meaningful with SPEC-06's
multi-workspace server), and persist to `.sage/jobs.jsonl`
(parameters only, never source content). On restart: pending jobs
resume, running jobs are marked `interrupted` — never silently
"running" forever.

## Graceful shutdown

SIGTERM stops accepting, drains up to `--drain-timeout` (in-flight job
completes or is marked interrupted), shuts down MCP, snapshots metrics,
closes stores, and releases the workspace lock — last.

## Rate limiting

The server construction exposes a middleware slot
(`func(next http.Handler) http.Handler`) with a no-op default and a
per-IP token-bucket example (`serve.TokenBucket`). Policy lives with the
operator; the example returns 429 with the `rate_limited` envelope.
