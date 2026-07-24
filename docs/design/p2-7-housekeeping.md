# Design: P2-7 — Housekeeping bundle

**Status:** shipped (feat/p2-7-housekeeping)

> Review log: spec 2 rounds STOP_CLEAN (16 findings), plan 1 round
> STOP_CLEAN (6 findings). Program spec:
> `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md §P2-7`.

Four independent items, each its own commit-set and tests.

## 1. dist drift enforcement (MAINT-02)

The committed `internal/web/dist` drifted silently because the CI check
was advisory — and it was advisory because hosted Node 22 doesn't
byte-match assets built under `node:22-alpine`. Root cause fixed instead
of tolerated: the frontend job now runs in a `node:22-alpine` container
(apk bash+git step with `shell: sh` first — alpine has neither and the
workflow defaults to bash; `setup-node` dropped, the image IS node 22),
and the drift check hard-fails. Release binaries are untouched (they ship
no-webui today; uncommitting dist would change release contents — rejected
in favor of the program spec's option B). Regeneration is a documented
one-liner (README web UI section).

## 2. Price-table override (PERF-04)

`compiler.price_table` → JSON (same shape as the built-in map) merged
per provider/model over built-ins. `NewCostTrackerWithTable` (the old
constructor delegates with no table), wired into `newTrackedClient` and
`EstimateFromBytes` (tablePath param) so estimate-before-compile prices
identically to the final report. Missing/malformed file warns + falls
back — the table is optional, never a failure. Precedence:
`token_price_per_million` > table > built-in; prefix matching matches
built-in behavior. Report header reads "Cost report (approximate)".

## 3. README translation drift (MAINT-05)

Per-language may-lag headers (+ greppable `<!-- translations: may-lag -->`
marker) on the six translated READMEs, and
`scripts/check-readme-translations.sh` behind a CI job: a README.md-only
change fails unless a `README_*.md` moved with it or a commit message
carries `translations: lag-ok`. The script is env-driven with a
`--self-test` covering all four contract cases and a `--verify-headers`
mode; the CI job sets `fetch-depth: 0` and maps PR vs push ranges to
BASE/HEAD.

## 4. ANN vector index (PERF-01 follow-on)

`VectorIndex` seam via functional options on the Store: default is the
P1-5 brute-force cache; `search.ann.enabled: true` selects HNSW.

**The vendoring story (the load-bearing decision).** The program spec
names `coder/hnsw`. v0.6.1 proved broken for k>1 search, with two
independent defects reproduced before vendoring:

1. `heap.Max()` returns the last array slot of a **min**-heap — an
   arbitrary leaf, not the maximum (15-element repro: Max()=14 vs true
   15) — corrupting result-set and frontier eviction.
2. Deeper: the layer "search" is greedy hill-climbing that terminates
   when no neighbor beats the **current best** result — ~60% of
   self-queries failed on a 2000×64-dim corpus with healthy layer
   structure, even at efSearch=2000, and the same weak search builds
   neighborhoods during Add, poisoning the graph itself.

EfSearch sweeps and exact re-ranking could not recover recall (0–4/10).
So the library is vendored under `internal/vectors/hnsw` (CC0) with
`layerNode.search` rewritten as textbook ef-search (min-heap frontier +
sorted ef-capped result + frontier-vs-worst termination). Outcome: 0/20
failing graphs on self-queries; recall@10 vs exact = 6.1/10 @ef20,
9.0/10 @ef50, 9.8/10 @ef100 (adversarial uniform-random corpus), 8.7/10
@ef20 (clustered corpus). The backend runs ef=200 with a fixed-seed Rng
(reproducible graphs). `VENDORED.md` documents the bug, the fix, and the
restore-upstream gate (`hnsw/graph_search_test.go`). Upstream report-worthy.

**Cache integration:** the graph is a derived structure mirroring the
matrix — Store-owned writes patch it, invalidation rebuilds it from DB
rows, and caller-tx `UpsertChunk` keeps the existing
`InvalidateChunkCache` contract (the Store can't observe the caller's
commit). Filtered search over-fetches `limit*4` then filters (documented:
may return fewer than `limit` under extreme selectivity).

**Wiring:** `OpenOptions.ANN` threads storedial → sqlitestore →
`vectors.NewStore(db, WithANN(...))`; `app.Open` sets it from
`search.ann.enabled`; `internal/query`'s four inline constructors and
the hub's primary path (`search.go:98`) honor it; the nil-cfg fallback
stays brute-force by construction.

**Cost honesty:** graph build is ~100× matrix-load cost at 2k docs, paid
lazily on first search after open/invalidation; query-time ANN wins at
very large vaults (the stated opt-in). Matrix + graph coexist in v1
(memory overhead documented; matrix drop is a future optimization).

## Cross-cutting

- No CGO anywhere (vendored hnsw is pure Go; vek dependency via upstream).
- SQLite zero-config default untouched; ANN is opt-in only.
- CHANGELOG entries for all four items.
