# Vendored: github.com/coder/hnsw v0.6.1 (CC0 1.0 Universal)

Copied verbatim, then patched, on 2026-07-24 (sage-wiki P2-7).

## Why vendored (upstream bug, proven)

`heap.Max()` returns the last array slot of a MIN-heap — which is NOT the
maximum (proven with a 15-element repro: Max()=14 with true max 15).
`layerNode.search` used `result.Max()` + `PopLast()` to keep the k best
candidates and `candidates.PopLast()` to trim the exploration frontier,
so both the result set and the frontier lost good candidates arbitrarily.
Observed effect on a 2000×64-dim corpus: recall@10 vs exact brute-force
was 0–4/10 regardless of EfSearch — k>1 search was effectively broken.

## The fix

`layerNode.search` was REWRITTEN as textbook HNSW ef-search (Malkov &
Yashunin 2014, Algorithm 2): a min-heap frontier plus a sorted
ef-capped result list, terminating when the nearest frontier candidate
is farther than the worst kept result. This replaces BOTH upstream
defects wholesale (the eviction bug and the hill-climbing termination)
— the Max()/Remove(maxIdx) patch tried first is moot under the rewrite.
Also: upper-layer descent is greedy ef=1 (upstream ran full ef-search
per layer), and Graph.Add's replace-on-duplicate-key path was fixed
(delete before the loop; the old mid-loop delete could nil-deref the
elevator and panic the invariant check).

## Dependency note

The vendored package is NOT fully self-contained: distance.go uses
`github.com/viterin/vek` (v0.4.2, pinned in go.mod) for SIMD cosine
math. vek is pure Go with a noasm fallback — no CGO anywhere.

## Not vendored

encode.go (Export/Import — no persistence needed, graphs rebuild at
open), analyzer.go, SavedGraph, tests.

## Later fixes (independent review, 2026-07-24)

- **Delete left empty layers** → nil-pointer panics on later Add
  (assertDims→Dims→entry().Value) and Search (empty top layer). Fixed:
  trailing empty layers pruned in Delete, Dims()/assertDims nil-tolerant,
  Search skips empty layers defensively.
- **Ghost nodes after Delete.** Two resurrection paths: (1) isolate()
  removed links and called replenish in ONE pass — replenish re-added the
  deleted node through neighbors not yet cleaned; (2) the graph-level
  ghost sweep made the same mistake one level up (delete+replenish per
  node in map order). Worse, replenish() creates ONE-WAY links, which
  isolate() cannot clean at all. Fixed: two passes everywhere (unlink
  fully, then replenish), plus a full neighbor-map sweep in Delete.
- Upstream's `heap.Max()`/`PopLast()` (the proven-broken API) is DELETED
  from this copy so it can't be re-misused.

### Known latent quirks (documented, not bugs today)

- `replenish()` hard-codes `CosineDistance` rather than the graph's
  configured `Distance` (upstream quirk). Nothing changes `Distance` in
  this codebase; if that ever happens, replenish must take it.
- After a full Delete-prune, `Dims()==0` makes `assertDims` a no-op, so
  the next Add redefines dimensionality (previously a mismatch panic).
  Consistent with the store-level dimension guard; noted for future
  direct-Graph callers.

## Upstream

Report-worthy: github.com/coder/hnsw — heap.Max() min-heap fallacy in
layerNode.search. If upstream fixes it, this directory can be dropped
and the dependency restored (keep the recall test as the gate: 5
independently seeded 2000×64-dim graphs × 7 self-query probes at
ef=100 — reduced from 20 graphs in 2026-07 for CI wall time; detection
margin against the gated 60%-failure defect remains overwhelming).
