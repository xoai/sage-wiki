# Design: P2-2 — Observability

**Status:** draft (first commit of PR per Phase-2 spec preamble)
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-2
**Cycle:** `.sage/work/20260721-p2-2-observability/`

## 1. Problem

No measurements exist for the 100K-doc performance story: compile pass
durations, token spend per pass, retry/429 pressure, backpressure dynamics,
search latency, and cache effectiveness are all invisible. Spec requires:
structured logs always, optional Prometheus `/metrics` behind a flag,
dependency-light, off by default, no CGO.

## 2. Design decisions

### D1 — Zero-dependency registry in `internal/metrics`

One package: `Counter` (atomic.Int64), `Gauge` (atomic.Int64),
`Histogram` (fixed bucket boundaries, per-bucket atomic counts + sum +
count). A package-level default `Registry` plus per-domain named instances
if isolation is needed (decision: ONE default registry — metric names carry
the domain prefix, no multi-registry complexity).

**Disabled fast path:** one package-level `atomic.Bool enabled`. Every
recording method starts with `if !enabled.Load() { return }`. Nil-safety:
the zero `*Counter`/`*Gauge`/`*Histogram` (nil pointer) is valid — recording
on nil returns immediately, so hook sites captured before registry init
never panic. Overhead-when-disabled = one atomic load; proven by a
benchmark in the package.

### D2 — No new dependencies; hand-rolled text exposition

The Prometheus text format is ~40 lines to emit correctly (escaping,
HELP/TYPE lines, histogram `_bucket{le=}` series + `_sum` + `_count`).
Rejected: `prometheus/client_golang` (heavy dependency tree for a trivial
format) and OTel/OTLP (memory: otelhttp `url.full` leaks query-string
secrets; OTLP SDK inherits `OTEL_EXPORTER_OTLP_INSECURE` from env as a
shared baseline — both documented gotchas; also heavier than the task
needs). The exposition handler is a `http.Handler` returning `text/plain;
version=0.0.4`.

### D3 — Cardinality discipline (security + cost)

No user-controlled label values, ever. Labels limited to fixed enums:
`pass` (summarize|extract|write|index|embed), `stage` (bm25|vector|rrf),
`provider` (config enum), `result` (ok|error|429). Query text, source
paths, IDs, and doc names are NEVER label values (they would be both a
cardinality explosion and an information leak). A compile-time-enforced
convention: label values are declared as package constants, and the
registry API takes a small fixed `Labels map` — tests assert the registered
series count stays bounded.

### D4 — The metric set (spec-mandated, no more)

| Name | Type | Hook |
|---|---|---|
| `compile_pass_duration_seconds{pass}` | histogram | compiler pass ends (pipeline.go/fullpipeline.go) |
| `llm_tokens_total{pass,direction}` | counter | CostTracker.Track (surfaced, not re-instrumented) |
| `llm_retries_total{result}` | counter | retry loop + 429 path in internal/llm |
| `backpressure_limit` | gauge | BackpressureController limit changes |
| `backpressure_in_flight` | gauge | BackpressureController acquire/release |
| `search_duration_seconds{stage}` | histogram | hybrid.Searcher stage boundaries |
| `query_duration_seconds` | histogram | query.Query end |
| `embed_calls_total` | counter | embed.Embedder call sites (one wrapper, not per-caller) |
| `vector_cache_hits_total` / `vector_cache_misses_total` | counter | vectors cache load paths (doc + chunk) |

CostTracker surfacing: `Track` already records per-call usage; the counter
is incremented inside `Track` (one place) — no double accounting, no API
change.

### D5 — Delivery: logs always, endpoint optional

- **Logs:** a `Snapshot()` → map dump emitted via `log.Info("metrics", ...)`
  at compile phase ends and compile completion (2-3 lines per compile, not
  per pass). Uses internal/log's existing slog plumbing. Emitted even when
  `serve.metrics` is false (the registry is always-on for logging; the
  `enabled` flag only gates the ENDPOINT. **Correction to brief:** the
  registry is always live — overhead is tiny and the log snapshot needs the
  data; `serve.metrics` gates only the HTTP handler. The disabled-overhead
  benchmark therefore measures per-hook cost (~ns), which is the real
  acceptance criterion.)
- **Endpoint:** web server registers `GET /metrics` ONLY when
  `serve.metrics: true` (new `Metrics bool` field on ServeConfig). Localhost
  binding unchanged. Handler is also exposed for non-web embedding
  (TUI/CLI never serve it).

### D6 — Hook placement discipline

One-line calls at natural boundaries: `defer m.ObserveDuration(hist, start)`
pattern where a defer reads cleanly, direct `c.Inc()` at counters. No
middleware, no goroutines, no background flushers. Backpressure gauges set
at the points limit/inFlight already change (inside the controller — no
external polling).

### D7 — Config

```yaml
serve:
  metrics: true   # default false — registers /metrics on the web server
```

One field, no other config. Buckets are package constants (documented as
tunable-in-code, YAGNI for config).

## 3. Non-goals

Tracing, OTel, dashboards, alerting, per-source metrics, log format
changes, storage-seam instrumentation (beyond free `db.Stats()` if used),
background exporters.

## 4. Test strategy

- Registry unit tests: counter/gauge/histogram math, exposition format
  validity (parse against the text format spec: bucket lines, sum, count,
  escaping), nil-safety, disabled fast path.
- Hook tests: CostTracker.Track increments the counter exactly once per
  call; backpressure gauges track limit changes; snapshot emits at compile
  phase end (log capture).
- Endpoint test: `serve.metrics: true` → GET /metrics 200 + expected series
  names; false → 404.
- Overhead benchmark: `BenchmarkDisabledHook` shows atomic-load cost.
- Full suite + `CGO_ENABLED=0 go build ./...` green.
