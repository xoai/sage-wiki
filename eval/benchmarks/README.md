# Memory benchmarks for sage-wiki

Runs three published conversational-memory benchmarks — **LOCOMO**,
**LongMemEval**, and **BEAM** — against **sage-wiki as the memory system
under test**, using the datasets, prompts, and judging procedure of
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks)
(Apache-2.0, see `NOTICE`) with sage-wiki replacing Mem0 as the backend.

Results and analysis: **[REPORT.md](REPORT.md)**.

## How it works

Every conversation (LOCOMO), question haystack (LongMemEval), or chat
(BEAM) becomes its own sage-wiki project — the isolation analogue of Mem0's
`user_id`:

1. **Ingest** — sessions are rendered to markdown files in `<project>/raw/`
   (session dates embedded in headings and body text).
2. **Compile** — `sage-wiki compile` runs the real LLM pipeline: summaries,
   concept extraction, article writing, FTS5 + embeddings + ontology.
3. **Search** — `sage-wiki search <question> --format json --limit K`
   retrieves context: document- and chunk-level BM25 and vectors fused by
   weighted RRF with the ontology graph as a third channel, plus a recency
   tie-breaker on documents with a known origin date. The LLM stages
   (expansion, reranking) stay off unless the run asks for them.
4. **Answer + judge** — an answerer LLM writes an answer from the retrieved
   articles; a judge LLM scores it against ground truth using the mem0
   prompts verbatim.

Infrastructure failures (compile hard-fail, persistently degraded search)
are recorded as `infra_error` and **excluded from accuracy denominators** —
they are counted and reported separately, never silently scored wrong.

## Setup

```bash
# 1. Build the binary (repo root)
go build -o sage-wiki ./cmd/sage-wiki

# 2. Python env
python3 -m venv .venv && source .venv/bin/activate
pip install -r eval/benchmarks/requirements.txt

# 3. Key (needs funded quota; used for compile, embeddings, answerer, judge)
export OPENAI_API_KEY=sk-...
```

Datasets auto-download on first run (LOCOMO ~5 MB from GitHub;
LongMemEval-S ~265 MB from HuggingFace; BEAM via the `datasets` library)
into `eval/benchmarks/runs/datasets/` (gitignored).

## Running

All runners execute **from the `eval/` directory** (the package root):

```bash
cd eval

# Smoke first (a few questions end to end, ~2 minutes, cents)
python -m benchmarks.locomo.run      --project-name smoke --smoke
python -m benchmarks.longmemeval.run --project-name smoke --smoke
python -m benchmarks.beam.run        --project-name smoke --smoke

# Full scoped runs
python -m benchmarks.locomo.run      --project-name full --workers 6
python -m benchmarks.longmemeval.run --project-name full --per-type 5
python -m benchmarks.beam.run        --project-name full --chat-sizes 100K --conversations 0-2
```

Shared flags: `--binary` (default: `SAGE_WIKI_BIN` env, else the repo-root
`sage-wiki`), `--answerer-model` / `--judge-model` / `--compile-model`
(default `gpt-4o-mini`), `--top-k` (default 10), `--max-questions`,
`--workers`, `--smoke`.

### Long runs in the background

Runs are resumable (per-question checkpoint files; compiled projects are
skipped on rerun), so background them and check progress by file count:

```bash
cd eval
nohup python -m benchmarks.locomo.run --project-name full > locomo.log 2>&1 &

# progress: completed questions
ls benchmarks/runs/locomo/full/*.json | wc -l
# heartbeat lines appear in the log every 25 questions
tail -f locomo.log
```

Interrupting a run loses at most the questions in flight; rerunning the
same `--project-name` resumes from the checkpoints.

### Rate limits

Long runs will meet provider throttling. The harness handles it in three
layers, all shared process-wide (`common/ratelimit.py`):

- **Cooperative backoff.** A 429 seen by *any* worker opens a cooldown that
  *every* worker waits out before its next call, escalating 5 s → 10 s → …
  → 120 s and honoring the provider's `Retry-After` header when present. A
  successful call resets the escalation.
- **The search path counts too.** Query embeddings happen inside the
  `sage-wiki search` subprocess, so throttling surfaces as an exit-0
  "BM25-only" degrade rather than an exception. The harness recognizes the
  rate-limit flavor of that warning, routes it through the same gate, and
  gives it more retries than a permanent (config) degrade — which no amount
  of retrying can fix.
- **Clean abort instead of a burned queue.** If the wall persists past 15
  minutes of cumulative waiting with no successful call, the run stops with
  `QuotaExhausted` and records `status: aborted_quota` plus resume
  instructions in `_run_metadata.json`. Completed questions stay
  checkpointed; rerun the same `--project-name` when quota returns.

Rate-limit activity is reported in each run's aggregate JSON under
`metadata.rate_limit` (`rate_limit_events`, `total_wait_seconds`).

Without this, a mid-run quota wall silently converts the rest of the queue
into `infra_error` records — which is exactly what cost the 2026-07-28
parity run 1,011 of its 1,540 questions (see REPORT.md).

## Layout

```
eval/benchmarks/
├── common/        # LLM client, SageWikiBackend, checkpoints, metrics
├── locomo/        # runner + vendored prompts
├── longmemeval/   # runner + vendored prompts
├── beam/          # runner + vendored prompts
├── tests/         # offline pytest suite (no network, no real binary)
├── results/       # committed aggregate results (one JSON per run)
├── runs/          # gitignored working data: datasets, projects, checkpoints
├── REPORT.md      # detailed results report
└── report_check.py  # verifies REPORT.md numbers against results JSONs
```

Per-question records (retrieval, generated answer, judgment) live in
`runs/<benchmark>/<project-name>/*.json`; aggregates (metrics by
category/type, latency percentiles, token usage, compile stats) land in
`results/<benchmark>_<project-name>.json`.

## Scope and comparability

Default scope (cost-bounded; see REPORT.md for the full caveat list):

| Benchmark | Scope | Questions |
|---|---|---|
| LOCOMO | all 10 conversations, categories 1–4 | 1,540 |
| LongMemEval | `longmemeval_s`, stratified 5/type (seed 42) | 30 |
| BEAM | 100K bucket, conversations 0–2 | 60 |

Prompts, datasets, and judging procedure are identical to mem0's harness,
but absolute numbers are **not** directly comparable to mem0's published
table: this setup uses a gpt-4o-mini answerer/judge (not gpt-5), a single
top-10 retrieval cutoff (mem0 retrieves top-200 and evaluates cutoffs up to
200), and sage-wiki results carry no per-memory `created_at` (dates reach
the answerer only inside compiled article text).

## Tests

```bash
# from the repo root — fully offline
python -m pytest eval/benchmarks/tests -q
```

The suite covers prompt rendering shapes, the subprocess contract (including
the zero-hit `data:null` envelope and both degraded-search guards),
checkpoint resume, metrics (infra_error exclusion, Kendall tau-b), and each
runner end-to-end against stub backends.
