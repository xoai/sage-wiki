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

With `serve.metrics: true`, the web server (`sage-wiki serve --ui`)
exposes `GET /metrics` in Prometheus text format: search stage latencies
(BM25/vector/RRF), query latency, embedding calls, and vector cache
hit/miss for the serve process. Series appear only after their first
recording (no flat-zero noise). When a bearer token is configured,
`/metrics` is gated exactly like `/api/*`. The MCP transports (stdio and
SSE) never serve metrics — they are transports, not ops surfaces.

Overhead when the endpoint is off: a few nanoseconds of atomic ops per
hook — negligible by design and benchmarked
(`internal/metrics.BenchmarkHook`).

## Cardinality

Labels are pinned to fixed enums (pass, stage, direction, provider,
cache) — query text, source paths, and IDs are never label values, so
metrics are safe to expose and cheap to store.
