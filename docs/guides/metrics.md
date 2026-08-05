# Metrics and Observability

sage-wiki emits metrics two ways (P2-2): structured-log snapshots (always
on) and an optional Prometheus endpoint on the web server.

## Log snapshots

Every compile phase end, command return, and graceful shutdown emits a
`metrics snapshot` line with the current counters, gauges, and histogram
summaries. These cover the compile process (pass durations, LLM tokens,
retries, 429s, backpressure) and any search/embed work in the same
process. Registries are per-process: compile metrics live in the compile
process's log output, never on the endpoint.

## The /metrics endpoint

```yaml
# config.yaml
serve:
  metrics: true   # default false
```

With `serve.metrics: true`, the **web UI** (`sage-wiki serve --ui`) exposes
`GET /metrics` in Prometheus text format: search stage latencies (`stage="bm25"`,
`"vector"`, `"rrf"` per leg, and `"total"` for the end-to-end request through
the unified pipeline), query latency, embedding calls, and vector cache
hit/miss for the serve process. Series appear only after their first
recording (no flat-zero noise). When a bearer token is configured,
`/metrics` is gated exactly like `/api/*`. The MCP transports (stdio and
SSE) never serve metrics — they are transports, not ops surfaces.

SPEC-07 adds the operational series. **Serve mode** (`sage-wiki serve --addr`,
SPEC-02) registers `GET /metrics` **unconditionally** (no `serve.metrics`
flag) carrying the full set below; the web UI serves the same series once
`serve.metrics: true` is set:

| Series | Kind | Labels | Meaning |
|---|---|---|---|
| `compiles_total` | counter | `tier`, `outcome` | compile jobs; outcome ∈ completed/failed/interrupted/cancelled |
| `compile_duration_seconds` | histogram | `tier` | end-to-end compile job duration |
| `llm_tokens_total` | counter | `provider`, `model`, `pass`, `direction` | tokens; direction ∈ input/output/cached splits cached from uncached |
| `search_channel_duration_seconds` | histogram | `channel` | per-channel leg latency (`bm25`/`vector`/`graph`) |
| `workspaces_open` | gauge | — | open workspaces (multi-workspace manager) |
| `job_queue_depth` | gauge | — | pending serve compile jobs |
| `events_dropped_total` | counter | — | events dropped by bounded buffers |
| `mirror_ship_lag_seconds` | gauge | — | seconds since the last successful mirror ship pass |

Overhead when the endpoint is off: a few nanoseconds of atomic ops per
hook — negligible by design and benchmarked
(`internal/metrics.BenchmarkHook`).

## Cardinality

Labels are pinned to fixed enums (pass, stage, direction, provider,
cache, tier, outcome, channel — plus `model`, which is key-only like
`provider` because model names are provider-defined) — query text, source
paths, and IDs are never label values, so
metrics are safe to expose and cheap to store.
