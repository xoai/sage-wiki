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
| **LOCOMO** | stratified 150 q (seed 42), gpt-5 judge, top-50 | **92.0%** LLM-judge accuracy | 0 |
| **LongMemEval-S** | 5/type (seed 42), 30 q, gpt-5 judge, top-50 | **93.3%** LLM-judge accuracy | 0 |
| **BEAM** | 100K bucket, 60 q, gpt-5 judge, top-200 | **69.1%** mean nugget score | 0 |

These are the current numbers, measured after the `arch/search-upgrade`
pipeline landed and with gpt-5 as judge/answerer (mem0's configuration). The
earlier figures — LOCOMO 76.8%, LongMemEval 63.3%, BEAM 27.1% — were taken
with the pre-upgrade search and a gpt-4o-mini judge, and are kept below for
provenance. Both variables changed between the two sets, so the improvement
cannot be attributed to search alone; the depth-curve analysis is what
isolates retrieval.

The dominant finding across all three is that **retrieval, not compilation,
was the binding constraint.** Every number above comes from the *same
compiled projects* as the earlier run — only search and the judge changed.
Abilities that previously read 0.0% (BEAM information_extraction,
temporal_reasoning) now read 100% and 54.2%, and accuracy is now nearly flat
across retrieval depth (+0.0pp to +0.5pp from top-10 to top-200, versus
+51.4pp before). The detail was in the compiled wiki all along; the old
search could not surface it.

## Side-by-side with Mem0's published numbers

Two axes are now matched — **gpt-5-class judge/answerer** and **mem0's
cutoff ladder** — which makes this far closer to a like-for-like reading than
the first version of this report. What still differs: mem0 runs their
**managed platform (v3 pipeline)** on **full question sets**, while this is
the **self-hosted sage-wiki binary** on **scoped samples** (150 / 30 / 60
questions), the compile pipelines are different by construction, and
sage-wiki results carry no per-memory timestamps (caveat 2). Sample sizes
mean ±4–5pp on the headline figures and much wider on per-category cells.
Mem0 column values are transcribed from the mem0ai/memory-benchmarks README
("Mem0 Platform" results section, local checkout, 2026-07-28).

| Benchmark | sage-wiki (gpt-5, scoped sample) | Mem0 Platform (published) |
|---|---|---|
| LOCOMO | **92.0%** @ top-50, **91.3%** @ top-200 (150 q) | **91.8%** @ top-50, **92.5%** @ top-200 (1,540 q) |
| LongMemEval-S | **93.3%** @ top-50, **90.0%** @ top-200 (30 q) | **94.8%** @ top-50, **94.4%** @ top-200 (500 q) |
| BEAM | **0.691** avg nugget — **100K** bucket, 60 q | **0.641** avg — **1M** bucket, 700 q; 0.486 — **10M**, 200 q (no 100K published) |

On LOCOMO the two are within a point at both cutoffs, and mem0's figures sit
inside this run's 95% confidence intervals. LongMemEval trails by 1.5–4.4pp
on a 30-question sample whose interval (74–97%) spans mem0's value. BEAM is
not comparable at all on levels — different size buckets (100K vs 1M/10M),
and mem0 published no 100K number.

Per-category LOCOMO (Mem0's breakdown is averaged across their top-10–200
cutoffs; ours is top-50, n in parentheses):

| Category | sage-wiki | Mem0 Platform |
|---|---|---|
| single-hop | 93.9% (82) | 91.2% |
| multi-hop | 85.7% (28) | 91.3% |
| temporal | 96.8% (31) | 92.0% |
| open-domain | 77.8% (9) | 72.7% |

The gap the first version of this report attributed to missing timestamps
has largely closed: **temporal was 61.1% and is now 96.8%**, above mem0's
92.0%, without any change to how dates are stored. That is a caution about
the earlier diagnosis — the absent `created_at` (caveat 2) is real, but it
was not what those numbers were measuring. Multi-hop is now the widest
remaining gap, on n=28.

BEAM by ability type (⚠ different size buckets — ours 100K, theirs 1M — so
read the *ordering*, not the levels):

| Ability | sage-wiki (100K, top-200) | Mem0 Platform (1M) |
|---|---|---|
| information_extraction | 1.000 | 0.700 |
| instruction_following | 1.000 | 0.852 |
| knowledge_update | 0.917 | 0.650 |
| preference_following | 0.833 | 0.883 |
| multi_session_reasoning | 0.729 | 0.652 |
| event_ordering | 0.678 | 0.536 |
| summarization | 0.674 | 0.635 |
| temporal_reasoning | 0.542 | 0.618 |
| contradiction_resolution | 0.375 | 0.357 |
| abstention | 0.167 | 0.525 |

Both systems still rank instruction/preference following near the top and
contradiction_resolution near the bottom. The former floor abilities —
information_extraction and temporal_reasoning — have moved to the top and
middle respectively. **Abstention is now sage-wiki's weakest ability and the
one clear regression** (0.333 → 0.167): retrieving more plausible context
makes a model likelier to answer when the correct response is to decline,
and this judge is separately noisy on abstention rubrics in both directions
(caveat 3). It is the obvious next thing to look at.

### Post-search-upgrade run (gpt-5, mem0's cutoff ladder)

After the `arch/search-upgrade` pipeline landed, LOCOMO was re-run with
**gpt-5 as both answerer and judge** (mem0's configuration) over a
**stratified 150-question sample** (seed 42; category shares within 0.4pp of
the full 1,540 and 14–16 questions from each of the ten conversations), at
mem0's cutoffs. Compiled projects were reused, so this measures the search
change, not a re-compile.

| Cutoff | Accuracy | 95% CI |
|---|---|---|
| top-10 | **87.3%** (131/150) | 81.1 – 91.7 |
| top-50 | **92.0%** (138/150) | 86.5 – 95.4 |
| top-200 | **91.3%** (137/150) | 85.7 – 94.9 |

<!-- check:locomo_gpt5-150 metrics_by_cutoff.top_10.overall.accuracy = 87.3 -->
<!-- check:locomo_gpt5-150 metrics_by_cutoff.top_50.overall.accuracy = 92.0 -->
<!-- check:locomo_gpt5-150 metrics_by_cutoff.top_200.overall.accuracy = 91.3 -->
<!-- check:locomo_gpt5-150 metrics.overall.infra_errors = 0 -->

| Category | n | top-10 | top-50 | top-200 |
|---|---:|---:|---:|---:|
| single-hop | 82 | 90.2% | 93.9% | 93.9% |
| multi-hop | 28 | 89.3% | 85.7% | 89.3% |
| temporal | 31 | 87.1% | 96.8% | 90.3% |
| open-domain | 9 | 55.6% | 77.8% | 77.8% |

#### LongMemEval and BEAM, same configuration

Both were re-run with gpt-5 on both roles at the same cutoffs, reusing their
compiled projects. **These corpora are 460–860 chunks**, so unlike LOCOMO
(35–100 chunks, where top-200 returned everything) top-200 is a genuine
retrieval cutoff here — which makes their depth curves the stronger evidence.

| Benchmark | top-10 | top-50 | top-200 | prior published |
|---|---:|---:|---:|---:|
| LongMemEval (30 q, accuracy) | **90.0%** | **93.3%** | **90.0%** | 63.3% |
| BEAM 100K (60 q, mean nugget) | **68.6%** | **67.2%** | **69.1%** | 27.1% |

<!-- check:longmemeval_gpt5 metrics_by_cutoff.top_10.overall.accuracy = 90.0 -->
<!-- check:longmemeval_gpt5 metrics_by_cutoff.top_50.overall.accuracy = 93.3 -->
<!-- check:longmemeval_gpt5 metrics_by_cutoff.top_200.overall.accuracy = 90.0 -->
<!-- check:beam_gpt5 metrics_by_cutoff.top_10.overall.avg_score = 68.6 -->
<!-- check:beam_gpt5 metrics_by_cutoff.top_50.overall.avg_score = 67.2 -->
<!-- check:beam_gpt5 metrics_by_cutoff.top_200.overall.avg_score = 69.1 -->

Depth sensitivity is **+0.0pp** (LongMemEval) and **+0.5pp** (BEAM) from
top-10 to top-200. Finding the needed material in the first ten of ~500
candidates is a real retrieval result, not a corpus-size artifact.

BEAM's per-ability movement is where the character of the change shows:

| Ability | now (top-200) | prior |
|---|---:|---:|
| information_extraction | **100.0%** | 0.0% |
| instruction_following | **100.0%** | 58.3% |
| knowledge_update | **91.7%** | 16.7% |
| preference_following | **83.3%** | 70.8% |
| multi_session_reasoning | **72.9%** | 16.7% |
| event_ordering | **67.8%** | 27.2% |
| summarization | **67.4%** | 35.6% |
| temporal_reasoning | **54.2%** | 0.0% |
| contradiction_resolution | **37.5%** | 12.5% |
| abstention | **16.7%** | 33.3% |

The two abilities that scored 0.0% before — information_extraction and
temporal_reasoning — were the report's headline evidence that compilation
discards verbatim detail. They now read 100% and 54.2%. That claim needs
revising: the detail was in the compiled wiki, and the old retrieval could
not reach it. **Abstention is the one regression** (33.3% → 16.7%), and it is
the ability where retrieving *more* plausible-looking context makes a model
likelier to answer when it should decline — consistent with caveat 3's note
that this judge is noisy in both directions on abstention rubrics.

Costs: LongMemEval $11.05 (30 q), BEAM $34.28 (60 q, 469 judge calls — its
rubric judge re-scores every nugget at every cutoff). Zero infra errors,
zero judge parse errors, zero rate-limit events across both.

**The headline is the shape, not the level.** In the pre-upgrade study below,
accuracy climbed **+51.4pp** from top-10 to top-200 — the answers were in the
wiki but a 10-result window did not surface them. After the upgrade that
dependency is **+4.0pp**, and top-200 is marginally *below* top-50 (extra
context distracts slightly). The new pipeline puts the right material in the
first ten results, which is exactly what a retrieval improvement should look
like. This within-run comparison is the robust part: the answerer is held
constant inside each curve, so the collapse from 51pp to 4pp is a property of
retrieval, not of the model reading it.

**What cannot be attributed cleanly:** top-10 went 43.5% → 87.3% between the
two studies, but *two* variables changed (old→new search AND gpt-5-mini→gpt-5
answerer), and the earlier study covered conversations 0–3 rather than a
stratified sample. The isolated search delta needs the old binary re-run on
this identical 150-question sample — the sampler is seeded precisely so that
comparison stays available.

**Versus Mem0** (92.5% @ top-200, gpt-5-class judge, full 1,540): mem0's
figure sits **inside this run's 95% confidence interval** at both top-50 and
top-200, so on this sample the two are statistically indistinguishable.
Restraint is still warranted — n=150 (±4–5pp), the compile pipelines differ,
and per-category cells are thin (open-domain n=9 is directional only).

Run cost: **$24.59**, 28 minutes, 0 infra errors, 0 judge parse errors, 0
rate-limit events.

### Partial parity study (pre-upgrade): which axis actually moves the number?

A follow-up LOCOMO run used **gpt-5 as judge, gpt-5-mini as answerer, and
mem0's cutoff ladder (top-10 / 50 / 200)** over the same compiled projects.
It was **cut short by API quota exhaustion after 529 of 1,540 questions**;
every question after that point degraded to BM25-only search and was
recorded `infra_error` and excluded rather than scored (`results/
locomo_parity.json`, 1,011 excluded). **The 529 completed questions are
conversations 0–3, not a random sample, so none of the percentages below
are sage-wiki's LOCOMO score** — they are only valid *against each other*,
because every row is the same question under both pipelines.

Same 529 questions, four pipelines:

| Pipeline | Accuracy |
|---|---|
| gpt-4o-mini judge @ top-10 (this report's config) | 67.1% |
| gpt-5 judge @ top-10 | **43.5%** |
| gpt-5 judge @ top-50 | **77.7%** |
| gpt-5 judge @ top-200 | **94.9%** |

<!-- check:locomo_parity metrics_by_cutoff.top_10.overall.accuracy = 43.5 -->
<!-- check:locomo_parity metrics_by_cutoff.top_50.overall.accuracy = 77.7 -->
<!-- check:locomo_parity metrics_by_cutoff.top_200.overall.accuracy = 94.9 -->
<!-- check:locomo_parity metrics.overall.infra_errors = 1011 -->

(The 67.1% baseline row is `results/locomo_full.json` restricted to those
529 question IDs; the three gpt-5 rows are machine-checked above.)

By category, same 529 questions:

| Category | n | 4o-mini @10 | gpt-5 @10 | gpt-5 @50 | gpt-5 @200 |
|---|---:|---:|---:|---:|---:|
| single-hop | 256 | 74.2% | 57.4% | 82.0% | 96.1% |
| multi-hop | 111 | 67.6% | 47.7% | 79.3% | 93.7% |
| open-domain | 32 | 65.6% | 59.4% | 62.5% | 75.0% |
| temporal | 130 | 53.1% | 8.5% | 71.5% | 98.5% |

Three findings, all of which change how the Mem0 table above should be read:

1. **Retrieval depth dominates everything else.** Holding the judge fixed,
   top-10 → top-200 moves accuracy +51pp (43.5% → 94.9%). The information
   is in the compiled wiki; a 10-result window simply does not surface it.
   Our headline 76.8% is a *shallow-retrieval* number, not a capability
   ceiling.
2. **gpt-5 is a much stricter judge than gpt-4o-mini** — at identical
   top-10 retrieval it scores the same answers 43.5% vs 67.1% (−24pp), and
   on temporal questions 8.5% vs 53.1%. So this report's gpt-4o-mini
   numbers are *judge-inflated* relative to Mem0's gpt-5-class judging: the
   real gap at equal depth is wider than the headline table implies, and
   the direction of the judge-model caveat (caveat 1) is now measured, not
   assumed.
3. **At these corpus sizes "top-200" means "the whole corpus."** A LOCOMO
   project compiles to roughly 35–100 searchable chunks, so a top-200
   request returns everything and the top-200 column measures *reasoning
   over the full compiled wiki with retrieval removed as a variable* —
   which is why temporal jumps to 98.5%. It is an upper bound on what the
   compiled representation preserves (and evidence that compilation keeps
   more temporal detail than the top-10 numbers suggest), not a retrieval
   result.

The honest summary: at matched retrieval depth and judge, sage-wiki's gap
to Mem0 on LOCOMO is much smaller than the headline table shows, but this
report's own default configuration (top-10, cheap judge) is not the
configuration that demonstrates it. Completing the remaining 1,011
questions — the run is resumable, `--project-name parity` picks up where it
stopped — is the outstanding work needed for a publishable head-to-head.

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
4. **The compile step is the memory bottleneck, by design** — *this claim
   was substantially wrong and the post-upgrade runs corrected it.* The
   pre-upgrade sections below argue that compilation discards verbatim
   detail, citing BEAM's information_extraction and temporal_reasoning at
   0.0%. Both now score 100% and 54.2% against the *same compiled
   projects*, changing only retrieval. The detail was in the compiled wiki;
   the old search could not reach it. sage-wiki is still a knowledge
   compiler rather than a verbatim log store, and the 94.7%→44.7% conv0
   delta still shows compiled-vs-raw retrieval differs — but "compilation
   loses the details" is not what the earlier numbers were measuring.
   Retrieval was.
5. Compile used `gpt-4o-mini` for all passes — a stronger compile model
   would likely lift every number; that axis is unexplored here.

## Reproducing

See [README.md](README.md). Short version: build the binary, `pip install
-r requirements.txt`, export a funded `OPENAI_API_KEY`, then run the three
runners from `eval/` (`--smoke` first). Verify this report's numbers with
`python3 eval/benchmarks/report_check.py`. Raw per-run data: committed
aggregates in `results/`; full per-question records (including retrieved
article text) are regenerated under `runs/` when you re-run.
