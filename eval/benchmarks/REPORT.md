# Memory benchmark report — sage-wiki v0.2.1

**Date:** 2026-07-28 · **System under test:** sage-wiki (built from `main`,
v0.2.1 lineage) as a conversational memory system · **Harness:**
`eval/benchmarks/` (this directory), adapted from
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks)
(Apache-2.0) with sage-wiki replacing Mem0 as the backend.

Numbers in this report are machine-verified against the JSONs in
[`results/`](results/) — run `python3 eval/benchmarks/report_check.py`.

## TL;DR

| Benchmark | Scope | Score | Infra errors |
|---|---|---|---|
| **LOCOMO** | 10 conversations, categories 1–4, **1,540 questions** | **76.8%** LLM-judge accuracy | 0 |
| **LongMemEval-S** | stratified 5/type (seed 42), **30 questions** | **63.3%** LLM-judge accuracy | 0 |
| **BEAM** | 100K bucket, conversations 0–2, **60 questions** | **27.1%** mean nugget score | 0 |

The pattern is consistent across all three: sage-wiki's compile-then-search
pipeline is **strong at semantic/factual recall** (LOCOMO single-hop 83.2%,
BEAM preference-following 70.8%) and **weak at verbatim detail and temporal
precision** (LOCOMO temporal 61.1%, BEAM information-extraction and
temporal-reasoning 0%), because the LLM compile step abstracts conversations
into concepts and articles — retaining meaning, discarding specifics like
exact dates and numbers.

## Side-by-side with Mem0's published numbers

For orientation only — **these columns are not an apples-to-apples
comparison.** The runs share prompts, datasets, and judging procedure, but
differ on every other axis that moves benchmark numbers: Mem0's published
results use their **managed platform (v3 pipeline)** with a **gpt-5-class
judge/answerer** at **top-200 retrieval** on **full question sets**; this
report uses the **self-hosted sage-wiki binary** with a **gpt-4o-mini**
judge/answerer at **top-10** on scoped samples (LongMemEval, BEAM), and
sage-wiki results carry no per-memory timestamps (caveat 2). Mem0 column
values are transcribed from the mem0ai/memory-benchmarks README ("Mem0
Platform" results section, local checkout, 2026-07-28).

| Benchmark | sage-wiki (this report) | Mem0 Platform (published) |
|---|---|---|
| LOCOMO (1,540 q) | **76.8%** @ top-10, gpt-4o-mini judge | **92.5%** @ top-200, gpt-5-class judge (91.8% @ top-50) |
| LongMemEval-S | **63.3%** — 30-q stratified sample | **94.4%** — full 500 q @ top-200 (94.8% @ top-50) |
| BEAM | **0.271** avg nugget — **100K** bucket, 60 q | **0.641** avg — **1M** bucket, 700 q; 0.486 — **10M**, 200 q (no 100K published) |

Per-category LOCOMO (Mem0's breakdown is averaged across their top-10–200
cutoffs; ours is top-10 only):

| Category | sage-wiki | Mem0 Platform |
|---|---|---|
| single-hop | 83.2% | 91.2% |
| multi-hop | 76.6% | 91.3% |
| open-domain | 74.0% | 72.7% |
| temporal | 61.1% | 92.0% |

The deltas are informative even without strict comparability: sage-wiki is
within ~8pp on single-hop and effectively at parity on open-domain, while
**temporal (−31pp) and multi-hop (−15pp) carry nearly the whole gap** —
consistent with this report's headline finding (no per-memory timestamps +
compile-time abstraction of details) rather than with a uniformly weaker
retrieval stack. Mem0's own weakest LOCOMO category (open-domain, 72.7%) is
the one where sage-wiki's compiled articles hold up best.

BEAM by ability type (⚠ different size buckets — ours 100K, theirs 1M —
so read only the *ordering*, not the levels):

| Ability | sage-wiki (100K) | Mem0 Platform (1M) |
|---|---|---|
| preference_following | 0.708 | 0.883 |
| instruction_following | 0.583 | 0.852 |
| summarization | 0.356 | 0.635 |
| abstention | 0.333 | 0.525 |
| event_ordering | 0.272 | 0.536 |
| knowledge_update | 0.167 | 0.650 |
| multi_session_reasoning | 0.167 | 0.652 |
| contradiction_resolution | 0.125 | 0.357 |
| information_extraction | 0.000 | 0.700 |
| temporal_reasoning | 0.000 | 0.618 |

Both systems rank preference/instruction following at the top and
contradiction_resolution near the bottom; the divergence is again verbatim
detail — information_extraction and temporal_reasoning, mid-pack for Mem0
(which stores extracted facts with timestamps), are floor for the compiled
wiki. Closing the judge-model and retrieval-depth gaps (run ours with a
gpt-5-class judge at top-200) is the prerequisite for any real
head-to-head; see caveat 1.

## Pipeline

One sage-wiki project per conversation/haystack (the `user_id` analogue):

1. **Ingest** — sessions rendered as markdown into `raw/` (session dates in
   headings and body).
2. **Compile** — `sage-wiki compile` (`compiler.mode: standard`): LLM
   summaries → concept extraction → article writing, FTS5 + OpenAI
   embeddings + ontology. Model: gpt-4o-mini; embeddings:
   text-embedding-3-small. The harness gates every project on a hardened
   compile-success predicate — the manifest's compiled-source count must
   equal the raw source count and vector entries must be ≥ the source count
   — before any question runs (see "Integrity incident" below for why).
3. **Search** — `sage-wiki search <question> --format json --limit 10`
   (hybrid BM25 + vector RRF over compiled articles/summaries).
4. **Answer + judge** — gpt-4o-mini answerer over the retrieved articles;
   gpt-4o-mini judge with mem0's prompts verbatim (binary judge for
   LOCOMO/LongMemEval; per-nugget 0/0.5/1 rubric for BEAM).

Infrastructure failures (compile hard-fail, degraded search) are recorded as
`infra_error` and excluded from accuracy denominators. **Across all 1,630
questions in the published runs there were 0 infra errors and 0 judge parse
errors.** Search-side degrade detection (stderr scan + a no-vector-ranked-
results guard) exists but is best-effort; the compile gate above is the
load-bearing defense that every project's *compiled layer* (summaries,
concepts, articles, embeddings) exists before questions run.

**What retrieval actually returns:** sage-wiki's hybrid index intentionally
contains raw source chunks alongside compiled content, so retrieved context
is a mix — in the larger LOCOMO conversations, 18–29% of retrieved entries
are raw transcript chunks; in the 19-source conversations, almost none.
That mix *is* the system under test. The incident below concerned a project
whose compiled layer was entirely missing, not this designed mix.

### Integrity incident (disclosed)

An earlier LOCOMO run published 81.8% overall. Independent review found that
conversation 0 (152 questions, 9.9%) had been benchmarked against a project
with **no compiled layer at all**: an interrupted compile left its work
queue leased, the restarted compile exited 0 having done nothing (raw
sources FTS-indexed, one stray vector, zero summaries/concepts/articles),
and the original `vector_count > 0` gate passed. Retrieval was therefore
pure verbatim transcript — which scored 94.7% on those questions; memory
benchmarks are much easier against unabridged text. The project was wiped,
recompiled through the hardened gate, and all 152 questions rerun; this
report carries only the corrected numbers. Properly compiled, conv0 scores
44.7% — its question set is heavily temporal/detail, the
compile-abstraction weak spot. LongMemEval and BEAM projects were audited
at the database level and were clean.

## LOCOMO — 76.8% overall

<!-- check:locomo_full metrics.overall.accuracy = 76.8 -->
<!-- check:locomo_full metrics.overall.total = 1540 -->
<!-- check:locomo_full metrics.overall.correct = 1183 -->

1,183 / 1,540 correct (76.8%) across all 10 conversations, categories 1–4.

| Category | Accuracy | n |
|---|---|---|
| single-hop | **83.2%** | 841 |
| multi-hop | **76.6%** | 282 |
| open-domain | **74.0%** | 96 |
| temporal | **61.1%** | 321 |

<!-- check:locomo_full metrics.by_group.single-hop.accuracy = 83.2 -->
<!-- check:locomo_full metrics.by_group.multi-hop.accuracy = 76.6 -->
<!-- check:locomo_full metrics.by_group.open-domain.accuracy = 74.0 -->
<!-- check:locomo_full metrics.by_group.temporal.accuracy = 61.1 -->

Temporal is the weakest category, as predicted by the design caveat:
sage-wiki search results carry no per-memory timestamp (`created_at` is
structurally absent), so the answerer sees dates only where the compile
preserved them inside article text. Per-conversation accuracy spreads
44.7%–86.9% — the low end (conv0) is dominated by exact-date and duration
questions the compiled wiki abstracts away.

Compile: the nine conversations compiled cleanly in the first run took
≈18.7 min for 253 session files (≈125 s per conversation); conversation 0's
clean recompile took 90.4 s (19 sources → 30 vectors, 11 concepts).

## LongMemEval-S — 63.3% overall

<!-- check:longmemeval_full metrics.overall.accuracy = 63.3 -->
<!-- check:longmemeval_full metrics.overall.total = 30 -->

19 / 30 correct on a stratified sample (5 per question type, seed 42). Each
question's ~50-session haystack (~115K tokens) was compiled into its own
project; the 30 haystack compiles plus 30 questions took ~46 minutes
wall-clock at 3 concurrent projects (per-project compile stats were not
recorded in this run's metadata — a known reporting gap).

| Question type | Accuracy |
|---|---|
| single-session-user | **100.0%** |
| single-session-assistant | **80.0%** |
| knowledge-update | **60.0%** |
| single-session-preference | **60.0%** |
| multi-session | **40.0%** |
| temporal-reasoning | **40.0%** |

<!-- check:longmemeval_full metrics.by_group.single-session-user.accuracy = 100.0 -->
<!-- check:longmemeval_full metrics.by_group.single-session-assistant.accuracy = 80.0 -->
<!-- check:longmemeval_full metrics.by_group.knowledge-update.accuracy = 60.0 -->
<!-- check:longmemeval_full metrics.by_group.single-session-preference.accuracy = 60.0 -->
<!-- check:longmemeval_full metrics.by_group.multi-session.accuracy = 40.0 -->
<!-- check:longmemeval_full metrics.by_group.temporal-reasoning.accuracy = 40.0 -->

Same shape as LOCOMO: user-fact recall is excellent; aggregation across many
sessions and temporal reasoning lose the most to compile-time abstraction.
**Small-sample caveat:** 5 questions per type → each cell has ±20pp
granularity; treat per-type numbers as directional.

## BEAM (100K) — 27.1% mean nugget score

<!-- check:beam_full metrics.overall.avg_score = 27.1 -->
<!-- check:beam_full metrics.overall.total = 60 -->

60 probing questions (3 conversations × 20; 10 ability types × 2 each) with
rubric-based per-nugget judging — the strictest of the three benchmarks.

| Ability type | Mean nugget score |
|---|---|
| preference_following | **70.8%** |
| instruction_following | **58.3%** |
| summarization | **35.6%** |
| abstention | **33.3%** |
| event_ordering | **27.2%** (with tau supplement: 46.9%) |
| knowledge_update | **16.7%** |
| multi_session_reasoning | **16.7%** |
| contradiction_resolution | **12.5%** |
| information_extraction | **0.0%** |
| temporal_reasoning | **0.0%** |

<!-- check:beam_full metrics.by_group.preference_following.avg_score = 70.8 -->
<!-- check:beam_full metrics.by_group.instruction_following.avg_score = 58.3 -->
<!-- check:beam_full metrics.by_group.summarization.avg_score = 35.6 -->
<!-- check:beam_full metrics.by_group.abstention.avg_score = 33.3 -->
<!-- check:beam_full metrics.by_group.event_ordering.avg_score = 27.2 -->
<!-- check:beam_full metrics.by_group.event_ordering.score_with_tau = 46.9 -->
<!-- check:beam_full metrics.by_group.knowledge_update.avg_score = 16.7 -->
<!-- check:beam_full metrics.by_group.multi_session_reasoning.avg_score = 16.7 -->
<!-- check:beam_full metrics.by_group.contradiction_resolution.avg_score = 12.5 -->
<!-- check:beam_full metrics.by_group.information_extraction.avg_score = 0.0 -->
<!-- check:beam_full metrics.by_group.temporal_reasoning.avg_score = 0.0 -->

BEAM probes exactly what LLM compilation discards. The rubric nuggets for
information_extraction and temporal_reasoning demand verbatim entities,
numbers, and date arithmetic from 100K-token conversations; after
summarize→extract→write compression, those details are usually gone from the
compiled articles, so the answerer cannot recover them at any retrieval
depth. Conversely, durable user preferences and standing instructions — the
kind of content the compile step promotes into concept articles — score
highest. **Small-sample caveat:** 6 questions per type.

## Latency

Search (subprocess spawn + OpenAI query embedding + hybrid RRF), per query:

| Run | p50 | p95 |
|---|---|---|
| LOCOMO | 959 ms | 1,142 ms |
| LongMemEval | 987 ms | 1,220 ms |
| BEAM | 959 ms | 1,428 ms |

<!-- check:locomo_full latency.p50_ms = 959.0 -->
<!-- check:longmemeval_full latency.p50_ms = 987.0 -->
<!-- check:beam_full latency.p50_ms = 958.7 -->

The ~1 s p50 is dominated by process startup and the query-embedding API
round-trip, not the search itself (sage-wiki's in-process hybrid search is
sub-millisecond at this corpus size — see `eval/REPORT.md`). A long-running
server (`sage-wiki serve`) would remove most of it.

## Cost (harness-tracked LLM usage)

Usage counters are **per-process**: a resumed run records only its own
calls, so totals below are the sum of the run processes and slightly
understate questions completed by interrupted attempts.

| Run | Answerer tokens (in/out) | Judge tokens (in/out) |
|---|---|---|
| LOCOMO (initial, 1,531 calls) | 9.60M / 0.64M | 1.14M / 0.06M |
| LOCOMO (conv0 rerun, 152 calls) | 0.88M / 0.06M | 0.11M / 0.007M |
| LongMemEval | 0.40M / 0.006M | 0.05M / 0.006M |
| BEAM | 0.42M / 0.016M | 0.18M / 0.006M |

At gpt-4o-mini pricing this is ≈ **$2.50** for all answer/judge calls
(including the 152 contaminated-run calls that were discarded and redone).
The compile side (sage-wiki's own LLM + embedding calls: ~10.5M input tokens
across 43 project compiles) is not metered by the harness; from token volume
it adds roughly **$2–4**. Total wall time: ~2.5 h for the initial runs plus
~25 min for the conv0 rerun.

## Caveats — read before comparing

1. **Not comparable to mem0's published table.** Prompts, datasets, and
   judging procedure are identical, but: judge/answerer are **gpt-4o-mini**
   (mem0 uses gpt-5-class judges); retrieval is a **single top-10 cutoff**
   (mem0 retrieves top-200 and evaluates cutoffs 10/20/50/200); LongMemEval
   and BEAM are **scoped samples** (30 and 60 questions), not full sets.
2. **No per-memory `created_at`.** sage-wiki's search results carry no
   timestamps, so the mem0 answer prompts render memories without date
   prefixes (LOCOMO shows "(unknown date)"). Dates reach the answerer only
   inside compiled article text. This structurally weakens temporal
   categories in all three benchmarks.
3. **The gpt-4o-mini judge errs in both directions on BEAM rubrics.** It
   sometimes scores a *correct* abstention as 0 (smoke-run `conv0_q0`: "I
   don't have enough information" matches the rubric's "there is no
   information related to X", scored 0.0) and sometimes scores a rubric
   *violation* as 1.0 (full-run `conv0_q0`: a nugget stating the information
   is absent scored 1.0 while praising the answer for providing it). Treat
   BEAM absolute numbers as judge-noisy; the per-type ordering is more
   robust than the levels.
4. **The compile step is the memory bottleneck, by design.** sage-wiki is a
   knowledge compiler, not a verbatim log store. These results measure
   sage-wiki's full retrieval surface — compiled articles and summaries
   plus the raw source chunks its index intentionally retains — which
   trades verbatim recall for organized, interlinked knowledge. The
   94.7%→44.7% conv0 delta between pure-transcript retrieval and the
   designed compiled-dominant mix (see the integrity incident) is a direct
   measurement of that trade-off on detail-heavy questions.
5. Compile used `gpt-4o-mini` for all passes — a stronger compile model
   would likely lift every number; that axis is unexplored here.

## Reproducing

See [README.md](README.md). Short version: build the binary, `pip install
-r requirements.txt`, export a funded `OPENAI_API_KEY`, then run the three
runners from `eval/` (`--smoke` first). Verify this report's numbers with
`python3 eval/benchmarks/report_check.py`. Raw per-run data: committed
aggregates in `results/`; full per-question records (including retrieved
article text) are regenerated under `runs/` when you re-run.
