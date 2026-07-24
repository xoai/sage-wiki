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

## The fix (graph.go, marked VENDORED FIX)

Result-set and frontier eviction now scan for the TRUE maximum distance
(O(k), k is small) and `Remove(maxIdx)` it. Everything else is verbatim:
graph construction, layer structure, distance functions.

## Not vendored

encode.go (Export/Import — no persistence needed, graphs rebuild at
open), analyzer.go, SavedGraph, tests.

## Upstream

Report-worthy: github.com/coder/hnsw — heap.Max() min-heap fallacy in
layerNode.search. If upstream fixes it, this directory can be dropped
and the dependency restored (keep the recall test as the gate).
