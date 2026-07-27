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
| **LOCOMO** | 10 conversations, categories 1–4, **1,540 questions** | **81.8%** LLM-judge accuracy | 0 |
| **LongMemEval-S** | stratified 5/type (seed 42), **30 questions** | **63.3%** LLM-judge accuracy | 0 |
| **BEAM** | 100K bucket, conversations 0–2, **60 questions** | **27.1%** mean nugget score | 0 |

The pattern is consistent across all three: sage-wiki's compile-then-search
pipeline is **strong at semantic/factual recall** (LOCOMO single-hop 87.3%,
BEAM preference-following 70.8%) and **weak at verbatim detail and temporal
precision** (BEAM information-extraction and temporal-reasoning 0%), because
the LLM compile step abstracts conversations into concepts and articles —
retaining meaning, discarding specifics like exact dates and numbers.

## Pipeline

One sage-wiki project per conversation/haystack (the `user_id` analogue):

1. **Ingest** — sessions rendered as markdown into `raw/` (session dates in
   headings and body).
2. **Compile** — `sage-wiki compile` (`compiler.mode: standard`): LLM
   summaries → concept extraction → article writing, FTS5 + OpenAI
   embeddings + ontology. Model: gpt-4o-mini; embeddings:
   text-embedding-3-small.
3. **Search** — `sage-wiki search <question> --format json --limit 10`
   (hybrid BM25 + vector RRF over compiled articles/summaries).
4. **Answer + judge** — gpt-4o-mini answerer over the retrieved articles;
   gpt-4o-mini judge with mem0's prompts verbatim (binary judge for
   LOCOMO/LongMemEval; per-nugget 0/0.5/1 rubric for BEAM).

Infrastructure failures (compile hard-fail, degraded search) are recorded as
`infra_error` and excluded from accuracy denominators. **Across all 1,630
questions there were 0 infra errors and 0 judge parse errors** — every
question was answered by the real hybrid pipeline (the harness verifies
per-search that the vector branch contributed; BM25-only degrades are never
scored).

## LOCOMO — 81.8% overall

<!-- check:locomo_full metrics.overall.accuracy = 81.8 -->
<!-- check:locomo_full metrics.overall.total = 1540 -->
<!-- check:locomo_full metrics.overall.correct = 1259 -->

1,259 / 1,540 correct (81.8%) across all 10 conversations, categories 1–4.

| Category | Accuracy | n |
|---|---|---|
| single-hop | **87.3%** | 841 |
| multi-hop | **81.2%** | 282 |
| open-domain | **76.0%** | 96 |
| temporal | **69.5%** | 321 |

<!-- check:locomo_full metrics.by_group.single-hop.accuracy = 87.3 -->
<!-- check:locomo_full metrics.by_group.multi-hop.accuracy = 81.2 -->
<!-- check:locomo_full metrics.by_group.open-domain.accuracy = 76.0 -->
<!-- check:locomo_full metrics.by_group.temporal.accuracy = 69.5 -->

Temporal is the weakest category, as predicted by the design caveat:
sage-wiki search results carry no per-memory timestamp (`created_at` is
structurally absent), so the answerer sees dates only where the compile
preserved them inside article text. Single- and multi-hop factual recall —
the thing a compiled, interlinked wiki is built for — is the strength.

Compile: 272 session files across 10 projects in 20.3 minutes total
(~122 s/conversation), producing 485 vector entries.

## LongMemEval-S — 63.3% overall

<!-- check:longmemeval_full metrics.overall.accuracy = 63.3 -->
<!-- check:longmemeval_full metrics.overall.total = 30 -->

19 / 30 correct on a stratified sample (5 per question type, seed 42). Each
question's ~50-session haystack (~115K tokens) was compiled into its own
project (~7–8 min each).

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
| LOCOMO | 960 ms | 1,147 ms |
| LongMemEval | 987 ms | 1,220 ms |
| BEAM | 959 ms | 1,428 ms |

<!-- check:locomo_full latency.p50_ms = 960.0 -->
<!-- check:longmemeval_full latency.p50_ms = 987.0 -->
<!-- check:beam_full latency.p50_ms = 958.7 -->

The ~1 s p50 is dominated by process startup and the query-embedding API
round-trip, not the search itself (sage-wiki's in-process hybrid search is
sub-millisecond at this corpus size — see `eval/REPORT.md`). A long-running
server (`sage-wiki serve`) would remove most of it.

## Cost (harness-tracked LLM usage)

| Run | Answerer tokens (in/out) | Judge tokens (in/out) |
|---|---|---|
| LOCOMO | 9.60M / 0.64M | 1.14M / 0.06M |
| LongMemEval | 0.40M / 0.006M | 0.05M / 0.006M |
| BEAM | 0.42M / 0.016M | 0.18M / 0.006M |

At gpt-4o-mini pricing this is ≈ **$2.30** for all answer/judge calls. The
compile side (sage-wiki's own LLM + embedding calls: ~10.4M input tokens
across 42 projects) is not metered by the harness; from token volume it adds
roughly **$2–4**. Total wall time: ~2.5 h (LOCOMO 55 min; LongMemEval 46 min
at 3 concurrent haystack compiles; BEAM 12 min).

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
3. **Judge-model weakness on abstention rubrics.** The gpt-4o-mini judge
   sometimes scores a *correct* abstention as 0 (e.g. BEAM `conv0_q0` in the
   smoke run: the answer "I don't have enough information" matches the
   rubric's "there is no information related to X", yet scored 0.0). BEAM
   abstention numbers are likely understated.
4. **The compile step is the memory bottleneck, by design.** sage-wiki is a
   knowledge compiler, not a verbatim log store. These results measure the
   compiled-wiki representation, which trades verbatim recall for
   organized, interlinked knowledge.
5. Compile used `gpt-4o-mini` for all passes — a stronger compile model
   would likely lift every number; that axis is unexplored here.

## Reproducing

See [README.md](README.md). Short version: build the binary, `pip install
-r requirements.txt`, export a funded `OPENAI_API_KEY`, then run the three
runners from `eval/` (`--smoke` first). Verify this report's numbers with
`python3 eval/benchmarks/report_check.py`. Raw per-run data: committed
aggregates in `results/`; full per-question records (including retrieved
article text) are regenerated under `runs/` when you re-run.
