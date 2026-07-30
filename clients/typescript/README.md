# sagewiki

Typed, zero-dependency TypeScript client for [sage-wiki](https://github.com/xoai/sage-wiki)'s `/v1` REST API.

> **Pre-1.0** — the API surface can change between releases. Pin a version:
> `npm install sagewiki@~0.1.0`

- **Zero runtime dependencies** — global `fetch` only.
- **Runs everywhere** — Node ≥18, Deno, Bun, Cloudflare Workers, browsers. No Node built-ins in the main entry, so edge runtimes work out of the box. That is a concrete advantage over subprocess-based integrations: this client deploys to the edge.
- ESM + CJS dual output with types; `sideEffects: false`.
- Compile-submit bodies are discriminated unions — mixing `topic` with compile flags is a **compile-time** error, not a 400 you discover at runtime.

## Install

```bash
npm install sagewiki
```

## Quickstart

Start the server: `sage-wiki serve --ui --port 3333` (loopback needs no
token; non-loopback sets `SAGE_WIKI_TOKEN`).

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient(); // SAGE_WIKI_URL / SAGE_WIKI_TOKEN from env, or pass { url, token }

// Search the compiled wiki + raw sources.
const results = await c.search("attention", { limit: 5 });
for (const r of results.results) console.log(r.finalScore, r.content.slice(0, 80));

// Compile-on-demand: uncompiledSources > 0 means matching sources are not
// yet compiled — submit a topic compile and wait.
if (results.uncompiledSources > 0) {
  const job = await c.compile({ topic: "attention" });
  await job.waitUntilDone({ timeoutMs: 600_000 }); // timeout REQUIRED
}

// Capture knowledge back (spends LLM budget on the server).
await c.capture("Self-attention computes pairwise token affinities.", {
  idempotencyKey: "note-1",
});
```

## Beyond search — the differentiators

This client is not an add/search pair:

```ts
// Provenance: which sources back a compiled article (and vice versa).
const prov = await c.provenance({ article: "attention" });

// Graph queries over the ontology — local neighborhoods or global
// community summaries, optionally time-scoped.
const answer = await c.graphQuery("how does attention relate to transformers", { hops: 2 });
// answer.answer, answer.cited, answer.seeds

// asOf needs ontology.temporal.enabled; mode: "global" needs
// ontology.communities.enabled — both off by default. Calling without them
// throws FeatureDisabled (412); FeatureDisabled.hint names the fix.
const history = await c.graphQuery("attention research", { asOf: "2026-01-01T00:00:00Z" });

// Read the compiled wiki directly — human-readable, Obsidian-compatible.
const article = await c.readArticle("concepts/attention.md");
```

## Errors

Branch on the class (or `.code`), never on `.message`:

| Class | HTTP | code |
|---|---|---|
| `InvalidArgument` | 400 | `invalid_argument` |
| `Unauthenticated` | 401 | `unauthenticated` |
| `Forbidden` | 403 | `forbidden` |
| `NotFound` | 404 | `not_found` |
| `Conflict` | 409 | `conflict` (`.activeJobId` when a compile is running) |
| `FeatureDisabled` | 412 | `feature_disabled` |
| `PayloadTooLarge` | 413 | `payload_too_large` |
| `RateLimited` | 429 | `rate_limited` (reserved) |
| `InternalError` | 500 | `internal` |
| `Unavailable` | 503 | `unavailable` |
| `JobTimeoutError` / `JobFailedError` | — | from `Job.waitUntilDone` |

Unknown future codes map to `SageWikiError` with the raw code intact.
`instanceof FeatureDisabled` and `switch (e.code)` both work.

## Async jobs

```ts
const job = await c.compile({ topic: "quantum computing", maxSources: 20 });
const full = await c.compile({ dryRun: true });          // full compile
const lint = await c.lint({ pass: "connections", fix: false });

await job.refresh();
await job.waitUntilDone({ timeoutMs: 300_000, pollIntervalMs: 2000 });
// rejects JobTimeoutError / JobFailedError; RESOLVES on cancelled
```

Submitting while a compile runs throws `Conflict` with `.activeJobId` —
poll that job instead. Pass `idempotencyKey` on submits and writes to make
retries safe (replays return the same `job_id`).

## AbortSignal, retries, timeouts

Every method accepts `signal`; job waiting honours it:

```ts
const ctl = new AbortController();
setTimeout(() => ctl.abort(), 5_000);
await c.search("attention", { signal: ctl.signal });
```

Transport errors and 503s are retried only when you opt in (`retries: N`),
and never for a write without an `idempotencyKey`. The HTTP request timeout
defaults to 30s (`timeoutMs`) and is separate from job-wait timeouts.

## Development

```bash
npm ci
npm run typecheck   # includes type-level tests (expect-error blocks)
npm test            # unit tests (mocked fetch)
eval "$(../../scripts/p4-fixture-server.sh)"
npm run test:contract   # contract tests against a live server
```
