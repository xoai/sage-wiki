# sagewiki

Typed Python client for [sage-wiki](https://github.com/xoai/sage-wiki)'s `/v1` REST API.

> **Pre-1.0** — the API surface can change between releases. Pin a version:
> `pip install "sagewiki==0.1.*"`

Sync and async clients, one shared implementation, `httpx` as the only
dependency, type hints throughout (`py.typed` shipped).

## Install

```bash
pip install sagewiki          # Python ≥ 3.9
```

## Quickstart

Start the server: `sage-wiki serve --ui --port 3333` (loopback needs no
token; non-loopback sets `SAGE_WIKI_TOKEN`).

```python
from sagewiki import SageWiki

c = SageWiki()  # SAGE_WIKI_URL / SAGE_WIKI_TOKEN from env, or pass url=/token=

# Search the compiled wiki + raw sources.
results = c.search("attention", limit=5)
for r in results.results:
    print(r.final_score, r.content[:80])

# Compile-on-demand: matches that are only raw sources surface as
# uncompiled_sources > 0 — submit a topic compile and wait.
if results.uncompiled_sources > 0:
    job = c.compile(topic="attention")
    job.wait(timeout=600)          # timeout is REQUIRED — no unbounded waits

# Capture knowledge back (spends LLM budget on the server).
c.capture("Self-attention computes pairwise token affinities.",
          idempotency_key="note-1")
```

Async is identical, with `await`:

```python
from sagewiki import AsyncSageWiki

async with AsyncSageWiki() as c:
    results = await c.search("attention")
    job = await c.compile(dry_run=True)
    await job.wait(timeout=60)
```

## Beyond search — the differentiators

This client is not an add/search pair. The parts worth knowing:

```python
# Provenance: which sources back a compiled article (and vice versa).
prov = c.provenance(article="attention")

# Graph queries over the ontology — local neighborhoods or global
# community summaries, optionally time-scoped.
answer = c.graph_query("how does attention relate to transformers", hops=2)
# answer.answer, answer.cited, answer.seeds

# as_of needs ontology.temporal.enabled, mode="global" needs
# ontology.communities.enabled — both off by default. Calling without them
# raises FeatureDisabled (412) and its docstring names the fix.
history = c.graph_query("attention research", as_of="2026-01-01T00:00:00Z")

# Read the compiled wiki directly — human-readable, Obsidian-compatible.
article = c.read_article("concepts/attention.md")
```

## Errors

Branch on the class (or `.code`), never on `.message`:

| Class | HTTP | code |
|---|---|---|
| `InvalidArgument` | 400 | `invalid_argument` |
| `Unauthenticated` | 401 | `unauthenticated` |
| `Forbidden` | 403 | `forbidden` |
| `NotFound` | 404 | `not_found` |
| `Conflict` | 409 | `conflict` (`.active_job_id` when a compile is running) |
| `FeatureDisabled` | 412 | `feature_disabled` |
| `PayloadTooLarge` | 413 | `payload_too_large` |
| `RateLimited` | 429 | `rate_limited` (reserved) |
| `InternalError` | 500 | `internal` |
| `Unavailable` | 503 | `unavailable` |
| `JobTimeout` / `JobFailed` | — | raised by `Job.wait()` |

Unknown future codes map to `SageWikiError` with the raw code intact.

## Async jobs

`compile()` and `lint()` submit jobs; they don't block:

```python
job = c.compile(topic="quantum computing", max_sources=20)
job = c.compile(dry_run=True)          # full compile (flags)
job = c.lint(pass_="connections", fix=False)

job.refresh()                          # poll once
job.wait(timeout=300, poll_interval=2) # poll until done — raises JobTimeout,
                                       # JobFailed; returns on cancelled
```

Submitting while a compile runs raises `Conflict` with `.active_job_id` —
poll that job instead. Pass `idempotency_key=` on submits and writes to
make retries safe (replays return the same `job_id`).

## Retries & timeouts

Transport errors and 503s are retried only when you opt in (`retries=N`),
and never for a write without an `idempotency_key`. The HTTP request
timeout defaults to 30s (`timeout=` on the constructor) and is separate
from `Job.wait(timeout=…)`.

## Development

```bash
pip install -e ".[dev]"
pytest -q                    # unit tests (mocked transport)
eval "$(../../scripts/p4-fixture-server.sh)"
pytest -q -m contract        # contract tests against a live server
```
