# Design: P2-2 — Observability

**Status:** draft, review iteration 4 (first commit of PR per Phase-2 spec preamble)

> Iteration log: i1 0C/4M/6S/2cos · i2 0C/4M/3S/1cos · i3 0C/5M/2S/1cos ·
> i4 0C/1M/5S/2cos — D3's stale enum paragraph survived two edits (replaced
> below with the full label inventory); shutdown-snapshot machinery
> unplaced; exposition registration timing unspecified; brief Risks stale
> on the auth gate.

**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-2
**Cycle:** `.sage/work/20260721-p2-2-observability/`

## 1. Problem

No measurements exist for the 100K-doc performance story: compile pass
durations, token spend per pass, retry/429 pressure, backpressure dynamics,
search latency, and cache effectiveness are all invisible. Spec requires:
structured logs always, optional Prometheus `/metrics` behind a flag,
dependency-light, off by default, no CGO.

## 2. Design decisions

### D1 — Zero-dependency registry in `internal/metrics`, always live

One package: `Counter` (atomic.Int64), `Gauge` (atomic.Int64),
`Histogram` (fixed bucket boundaries, per-bucket atomic counts + sum +
count). ONE package-level default registry — metric names carry the domain
prefix, no multi-registry complexity.

**The registry is always live** (i1 correction — the `enabled` fast-path
flag is dropped): log snapshots need the data, and "off by default" refers
to the HTTP endpoint only (D5). Overhead-when-endpoint-disabled = per-hook
atomic adds (~ns each), proven by `BenchmarkHook` in the package. Nil
safety: the zero `*Counter`/`*Gauge`/`*Histogram` (nil pointer) is valid —
recording on nil returns immediately, so hook sites captured before
registry init never panic.

### D2 — No new dependencies; hand-rolled text exposition

The Prometheus text format is ~40 lines to emit correctly (escaping,
HELP/TYPE lines, histogram `_bucket{le=}` series + `_sum` + `_count`).
Rejected: `prometheus/client_golang` (heavy dependency tree for a trivial
format) and OTel/OTLP (memory: otelhttp `url.full` leaks query-string
secrets; OTLP SDK inherits `OTEL_EXPORTER_OTLP_INSECURE` from env as a
shared baseline — both documented gotchas; also heavier than the task
needs). The exposition handler is a `http.Handler` returning `text/plain;
version=0.0.4`. Format rules pinned (the two classic hand-rolled bugs):
every histogram emits `le="+Inf"` exactly equal to `_count`; floats via
`strconv.FormatFloat(v, 'g', -1, 64)`; label values escaped for `\`, `"`,
`\n`; HELP/TYPE lines per series family.

### D3 — Cardinality discipline (security + cost)

No user-controlled label values, ever. The COMPLETE label inventory
(every label used in D4, nothing else): `pass` = summarize|extract|write;
`stage` = bm25|vector|rrf; `direction` = input|output|cached; `provider`
= the config provider enum; `cache` = doc|chunk. There is no `result`
label and no other pass value. Query text, source paths, IDs, and doc
names are NEVER label values (cardinality explosion + information leak).
Convention: label values are declared as package constants, the registry
API takes a small fixed Labels map, and the label-sync test asserts the
registered series stay within this inventory.

### D4 — The metric set (spec-mandated, no more)

| Name | Type | Hook |
|---|---|---|
| `compile_pass_duration_seconds{pass}` | histogram | compiler pass ends (pipeline.go/fullpipeline.go) |
| `llm_tokens_total{provider,pass,direction}` | counter | `CostTracker.Track` — the ONLY hook site (covers sync client.go:308 AND batch pipeline.go:963; batch is already tracked, no second site) |
| `llm_retries_total` | counter | retry ATTEMPTS, attempt-level, retry loop only |
| `llm_rate_limited_total` | counter | 429 responses, response-level, at each transport path (enumerated below) |
| `compile_backpressure_limit` | gauge | BackpressureController limit changes |
| `compile_backpressure_in_flight` | gauge | BackpressureController acquire/release |
| `search_duration_seconds{stage}` | histogram | hybrid.Searcher stage boundaries (bm25/vector/rrf) |
| `query_duration_seconds` | histogram | query.Query end |
| `embed_calls_total` | counter | embed.Embedder call sites (one wrapper, not per-caller) |
| `vector_cache_hits_total{cache}` / `vector_cache_misses_total{cache}` | counter | vectors cache load paths |

Pinned semantics (i1+i2):
- **Token hooking:** ONE site — `CostTracker.Track`. It already fires for
  sync calls (client.go:308) AND batch results (pipeline.go:963), so no
  second recording site exists anywhere (the i1 batch finding was a
  recording-site question, not a tracking gap). Re-resume caveat: a crash
  between RetrieveBatch and checkpoint retirement re-retrieves and
  re-counts — a pre-existing cost quirk the metric inherits, documented.
- **pass enum** = `summarize|extract|write` — the only values with
  `SetPass` sites. `query`/`lint` appear in cost.go's comment but no
  tracker is attached on those paths (query.go attaches none) — deferred,
  not in the label-sync test.
- **direction** = `input|output|cached` — `CachedTokens` is first-class.
- **429 counting is multi-site BY NECESSITY (i2):** the cached path
  (cache.go:90-93 — the DEFAULT for anthropic/gemini compiles) handles 429
  with a direct fallback that never passes through the direct loop's
  isRetryable branch. `llm_rate_limited_total` increments once per 429
  response at each enumerated transport site: client.go direct loop,
  cache.go cached fallback, stream.go, batch poll/retrieve. Sites are
  enumerated exhaustively in THIS document (D4 hook table); the label-sync test greps them.
- **Retries:** `llm_retries_total` per retry attempt, retry loop only.
- **Cache hit/miss** (`{cache=doc|chunk}`): hit = search served from the
  loaded in-memory matrix without a reload; miss = a lazy load/reload
  triggered. Invalidation does not count (it's not a miss, it's a flush).
- **Stage boundaries (i2-pinned):** `stage=bm25` = BM25 candidate fetch;
  `stage=vector` = vector candidate fetch EXCLUDING query embedding (done
  before Search); `stage=rrf` = fusion + hydration incl. the s.memory.Get
  lookups for vector-only hits.
- **trackUsage pass-context constraint:** `Client.SetPass` is unsynchronized
  and correct only because passes are sequential today; P2-3's worker model
  must make pass context explicit per worker (documented constraint on hook
  placement, not a new sync primitive).

### D5 — Delivery: logs always, endpoint optional

- **Logs:** a `Snapshot()` → map dump emitted via `log.Info("metrics", ...)`
  at compile phase ends and compile completion (2-3 lines per compile, not
  per pass). Uses internal/log's existing slog plumbing. Always emitted
  (registry is always live; "off by default" means the ENDPOINT).
- **Endpoint:** web server registers `GET /metrics` ONLY when
  `serve.metrics: true` (new `Metrics bool` field on ServeConfig). The
  handler is NOT build-tagged — it ships in the default (no-webui) binary.
  **Auth (i3):** token auth is path-based (`/api/*`, `/ws` only), so
  `/metrics` would be unauthenticated on a non-loopback bind — the gate is
  EXTENDED to cover `/metrics` (operational data), and the test pins both
  behaviors: loopback open, non-loopback auth-required.
- **Snapshots (D5 extension):** `Snapshot()` logs at compile phase ends,
  at compile completion, AND at process shutdown (covers search-only
  processes per D8).

### D8 — Process topology (i3-verified): which process serves which series

The registry is per-process, and the compile topology is verified:

- **CLI compile** (`sage-wiki compile`): own process; compile/token/
  backpressure series are **log-snapshot-only** (D5).
- **MCP stdio** (default transport): long-lived; compile-on-demand runs
  IN-PROCESS here (tools_compound.go:40, tools_write.go:671) — but stdio
  serves no HTTP, so those series are also **log-snapshot-only**.
- **MCP SSE**: long-lived, serves HTTP on 127.0.0.1, holds compile+search
  series. DECISION: `/metrics` is **NOT** registered on MCP SSE — it is an
  MCP transport, not an ops surface; adding a second metrics listener
  fragments the story. MCP-series stay log-snapshot-only.
- **Web server** (`serve [--ui]`): the ONLY `/metrics` host. It never
  compiles (verified: zero compile references in internal/web;
  `serve --ui` builds web only), so the endpoint exposes search/query/
  embed/cache series — plus `llm_retries_total`/`llm_rate_limited_total`
  when non-zero (query-time embedding retries happen in-process; i4).
  Compile-specific series (`compile_pass_duration_seconds`,
  `llm_tokens_total`, `compile_backpressure_*`) are absent here.
  **Registration timing:** histograms/counters register lazily on first
  recording — zero-activity series do NOT appear on the endpoint (no
  misleading flat-zero compile series in the serve process).
- **Search metrics in serve-less deployments** (MCP-only): silently
  accumulate with no endpoint — accepted and documented; a snapshot fires
  at process shutdown in addition to compile phase ends (D5), so the data
  is never lost, only non-live.
- A shared metrics file/daemon is rejected as over-engineering.

### D6 — Hook placement discipline

One-line calls at natural boundaries: `defer m.ObserveDuration(hist, start)`
pattern where a defer reads cleanly, direct `c.Inc()` at counters. No
middleware, no background flushers. Backpressure gauges set at the points
limit/inFlight already change (inside the controller — no external
polling). The D5 shutdown snapshot needs no signal machinery of its own:
it hooks the EXISTING graceful-shutdown paths (web server shutdown, MCP
server Close, CLI command return via one deferred call at the command's
top level) — placement enumerated in the spec, no new goroutines.

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
  validity (bucket lines, +Inf==count, sum, count, escaping, float format),
  nil-safety, `-race` concurrent recording.
- Hook tests: CostTracker.Track increments once per call (sync); batch
  usage reaches the counter at result retrieval; backpressure gauges track
  limit changes; snapshot emits at compile phase end (log capture);
  pass-label values stay within the pinned enum (label-sync test).
- Endpoint tests: `serve.metrics: true` → GET /metrics 200 + expected
  series; false → 404; handler present in the DEFAULT (no-webui) binary;
  auth gate — loopback open, non-loopback auth-required (D5);
  zero-activity series absent (lazy registration, D8).
- Overhead benchmark: `BenchmarkHook` shows per-hook atomic cost.
- Full suite + `CGO_ENABLED=0 go build ./...` green.
