# Design: P2-3 — Durable job model / compile worker

**Status:** shipped (feat/p2-3-compile-worker)

> Review log: spec 4 rounds STOP_CLEAN (20 findings), plan 2 rounds
> STOP_CLEAN (10 findings) — quality-locked v2 ledger at
> `.sage/work/20260724-p2-3-compile-worker/review-ledger.json`.
> Program spec: `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md §P2-3`.

## 1. Problem

`compile_items` was a state table with no claim semantics: compiles ran
synchronously per CLI invocation, `error_count` was written but never read,
progress went only to stderr, and serve mode could not compile without a CLI
process. Server mode needs compiles that run independently, survive worker
crashes, retry with a cap, and stream progress to web/TUI clients (STRAT-03).

## 2. Design

### Queue semantics on compile_items (migration V9 / pg V2)

`compile_items` — already the single resume truth post-P1-3 — gains claim
columns: `status` (pending/leased/done/failed), `lease_owner`,
`lease_until`, `heartbeat_at`, `attempts`, plus `idx_ci_claim`. Backfill is
per-tier conservative: only rows with every pass for their tier become
`done`. Both backends in lockstep, pinned by the conformance suite.

### Claim / release protocol (spec C2)

- **Claim** = candidate scan (existing per-tier pending predicate ∧
  `status != 'failed'` ∧ lease free-or-owned) + conditional UPDATE per
  candidate; a rows-affected-0 means another owner won — fencing without
  SKIP LOCKED so sqlite and postgres share the code path.
- **Lease** mirrors `internal/manifest/lock.go`: heartbeat interval (30s)
  ≪ TTL (120s), token-fenced (`pid-nanos-counter`).
- **attempts counts FAILED processing attempts, not claims** (refinement
  logged in cycle decisions.md): a retry release burns budget (+1), a done
  release resets it (0). Claims don't burn budget. Without this,
  partial-progress items (tier-3 with a down embedder but successful
  articles) and systemic outages would mass-dead-letter healthy sources.
- **Dead letter** at `attempts+1 >= max_attempts` on a failure release.
  Revival: `--fresh` (ResetFailed) OR a hash-changing Upsert (new content
  earns a fresh budget — a fixed source retries automatically).
- Embed failures are SOFT in this codebase (no MarkError), so an embed
  outage leaves items pending — CLI parity — while extract failures
  dead-letter as designed.

### Worker (internal/compiler/worker*.go)

One worker per serve process, started in `runServe` for BOTH MCP and
`--ui` modes. Per cycle (all inside `TryCompile(fn)` — the coordinator
release is the fn return):

1. RequeueExpired sweep (crash recovery; also at startup).
2. Batch guard — `hasPendingBatch` shared with watch mode.
3. Enqueue scan: manifest load + merge-base clone + `Diff` (NEVER
   loadInputs — its fresh-clearing and batch-resume handoff belong to the
   CLI), upsert via the shared `upsertDiffItems` helper, removed sources
   via `handleRemovedSources(prune=false)` + `DeleteByPaths`.
4. Claim tiers 0 → 1 → 3 (runTiers order).
5. Process through the EXISTING pass functions; heartbeat goroutine per
   cycle; per-item panic recovery (recover → release retry, worker
   survives).
6. Release each item once (errored → retry/failed, else done).
7. Promotion/demotion sweeps (runTiers parity).
8. Manifest MergeSave under the P1-2 lock on complete cycles; skip-save
   on tier-3-incomplete (P1-1/C1 analogue).
9. Sleep poll interval (outside fn). All-failed cycles hibernate with
   exponential backoff (poll × 2^streak, cap 30min).

### CLI = in-process worker of one

`runTiers` claims per tier with a `cli-<pid>-<nanos>` token, heartbeats
during passes (5min TTL / 30s beat), releases at pass end, requeues
expired leases at start, and resets dead letters under `--fresh` (never
under `--dry-run`). Claim/release is additive bookkeeping — pass
semantics, golden outputs, and checkpoint behavior are unchanged.

### Progress streaming (spec C6)

`compiler.Progress` gained a non-blocking subscriber hook (drop-on-full,
unsubscribe closes the channel). The CLI/TUI/worker share ONE hub per
process via `CompileOpts.Progress`. Surfaces:

- `GET /api/compile/progress` — SSE of ProgressEvents (headers flushed
  immediately or an idle queue deadlocks the client; unsubscribe on
  disconnect). 503 when no worker.
- `GET /api/compile/status` — JSON counts by status + active lease owner
  (CompileStats gains ByStatus/ActiveOwner/LastHeartbeat, both backends).
- TUI compile tab renders the live `[done/total] item` line from events;
  its 3s source-dir watch tick is untouched.
- `wiki_status` gains a `compile_queue` counts block.
- Queue transitions (claimed/requeued/dead-lettered) emit as `queue`
  events. On-demand `CompileTopic` compiles keep their own Progress and
  are NOT streamed (documented exclusion).

### Wiring (spec C4)

`runServe` assembles `serveDeps`: one CompileCoordinator (now passed into
the MCP server, replacing its private fallback so on-demand compiles
serialize against the worker), one Progress hub (passed to the web
server), and the worker on its own `app.Open` handle (queue fencing is
DB-level; two handles are safe). Config: `serve.worker.*` with
`enabled` as `*bool` (nil = true — the PromptCache precedent);
`heartbeat >= lease_ttl` is a hard validation error.

## 3. Behavior changes (all documented in CHANGELOG)

1. `serve` now compiles autonomously (worker default ON; opt out with
   `serve.worker.enabled: false`).
2. Persistently failing sources dead-letter after 5 attempts (was:
   retried forever). Revive with `--fresh` or by editing the source.
3. Dead-lettered items are skipped by claims until revived.

## 4. Non-goals

Batch mode (`batch-state.json`) untouched; no multi-process distributed
queue; no web compile-trigger endpoint; no reconciler/backpressure/tier
logic changes; no CGO; SQLite zero-config default untouched.
