# sage-wiki eval report

**Date:** 2026-07-28 · **Measured on:** 10 real compiled wikis (the LOCOMO
benchmark projects under `eval/benchmarks/runs/locomo/full/projects/`), built
by the actual compile pipeline with gpt-4o-mini and text-embedding-3-small.

> **This supersedes the April 2026 version of this file, which reported
> numbers from `eval_test.py`'s synthetic fixture generator, not from any real
> wiki.** Those figures (85.9% overall, 72.0% fact extraction, …) described the
> fixture's own generator parameters. Two bugs found while producing this
> report are why the distinction was invisible — see "Corrections" below.

## Scorecard across 10 real wikis

| Metric | Median | Range |
|---|---:|---|
| **Overall score** | **87.4%** | 82.5 – 89.9% |
| Fact extraction rate | 100.0% | 96.6 – 100.0% |
| Search recall@10 | 100.0% | 100.0 – 100.0% |
| Search recall@1 | 100.0% | 90.5 – 100.0% |
| Cross-reference integrity | 100.0% | — |
| Source citation rate | 100.0% | — |
| Concept coverage | 7.9% | 2.3 – 14.7% |

Each project is one LOCOMO conversation: ~19–32 source sessions compiled into
~27 concept articles plus summaries.

**Concept coverage deserves reading carefully — it is not a defect.** The
metric divides "concepts named in summaries" by "concepts with their own
article". On conv2 the summaries name **190** distinct concepts while the
extract pass produced **27** articles. That is deliberate consolidation, not
lost content: the alternative is 190 thin stubs. The scorecard treats higher
as better, which mis-frames what this number means; treat it as a
*consolidation ratio* and watch it for sudden movement rather than for level.

## Performance (in-process, conv2: 59 files, 91 entries, 231 chunks)

| Operation | p50 | Throughput |
|---|---:|---:|
| Graph BFS (depth 5) | 0.001 ms | 1,215,222 /s |
| FTS5 prefix | 0.011 ms | 50,487 /s |
| FTS5 MATCH top-1 | 0.024 ms | 34,230 /s |
| FTS5 MATCH top-10 | 0.035 ms | 26,356 /s |
| Vector similarity top-10 | 4.804 ms | 201 /s |
| Hybrid RRF | 4.877 ms | 204 /s |
| Sustained mixed reads | 0.005 ms | 143,407 ops/s |
| FTS write (batch 100) | — | 239,650 entries/s |

**This is the only place hybrid-search latency is visible.** The memory
benchmarks measure search at ~1.1 s p50, but that figure is dominated by
process spawn and the OpenAI embedding round-trip; the retrieval itself is the
~4.9 ms above. A regression in RRF or vector scan would be invisible in the
benchmark numbers and obvious here.

## Corrections to the previous report

Two bugs made the old numbers unrepresentative, and both were invisible
because the test fixtures reproduced the buggy assumptions:

1. **`eval.py` could not read any real project.** It hardcoded `_wiki/` while
   sage-wiki's scaffold emits `output: wiki`, so it exited 1 on every wiki the
   current binary produces. It now reads `output:` from `config.yaml` and
   falls back to `wiki/` then `_wiki/`. The fixtures generated `_wiki/`, so
   the suite stayed green while the tool was unusable.
2. **Fact extraction always scored 0% on real wikis.** It counted only bullet
   lines inside `## Key claims`, but the summarize pass writes prose
   paragraphs. A single run would report 0.0% fact extraction *and* 100%
   "Structural — Key claims" — two outputs of the same run contradicting each
   other. Claim counting is now format-agnostic (bullets, else sentences).
   Fixing it exposed a third latent bug: the section regex used `\s*\n`, which
   swallows blank lines after an *empty* section and captures the following
   heading's body. Bullet-only counting had masked it.

Real-wiki fact extraction is **100%**, not the 0% the broken metric reported
nor the 72% the fixture reported.

## How this relates to the memory benchmarks

`eval.py` and [`benchmarks/`](benchmarks/README.md) answer different questions
and neither replaces the other.

| | `eval.py` | `benchmarks/` |
|---|---|---|
| Question | Is this wiki well-formed and fast? | Can it answer questions correctly? |
| Cost | $0, seconds | ~$70, ~1 hour |
| Needs | any compiled project | published datasets + API keys |
| Runs on | *any* user's wiki | 3 fixed datasets |
| Unique signal | latency/throughput, broken links, orphans, citation rate, dedup | end-to-end answer accuracy vs ground truth |

A wiki can score 92% on LOCOMO while quietly accumulating broken wikilinks or
regressing 10× on search latency — neither shows up in the benchmarks. Run
`eval.py` cheaply and often (it is CI-able); run the benchmarks per release.

## Reproducing

```bash
# any compiled project
python3 eval/eval.py /path/to/wiki

# machine-readable, for tracking over time
python3 eval/eval.py --json /path/to/wiki > eval-$(date +%Y%m%d).json

# the projects used above (after running the LOCOMO benchmark)
python3 eval/eval.py eval/benchmarks/runs/locomo/full/projects/conv2
```

Tests: `python3 -m pytest eval/eval_test.py -q` (34 tests, offline).
