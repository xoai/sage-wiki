# Output Trust Guide

sage-wiki compiles sources into articles and answers questions via LLM
synthesis. But LLM answers are claims, not facts. Without safeguards,
an incorrect answer gets indexed and pollutes future queries -- each
wrong output compounds the error. The output trust system prevents this
by quarantining new outputs and requiring verification before they
enter the searchable corpus.

## The Problem

Consider this feedback loop:

1. You ask: "Who invented the transformer architecture?"
2. sage-wiki synthesizes an answer and indexes it as `wiki/outputs/...`
3. Next query: "What did Ashish Vaswani contribute?"
4. The previous (possibly wrong) output is now part of the search context
5. The new answer may cite the old output as a source

Each wrong answer compounds. The trust system breaks this loop by
treating outputs as pending claims until they earn trust through
grounding verification and consensus.

## Quick Start

Add to your `config.yaml`:

```yaml
trust:
  include_outputs: verified
```

That's it. New query outputs go to `wiki/under_review/` instead of
being indexed. Run `sage-wiki verify --all` periodically to check
pending outputs against their sources. Outputs that pass grounding
and consensus checks are promoted to `wiki/outputs/` and indexed.

## Configuration Reference

```yaml
trust:
  include_outputs: verified      # "false", "verified", or "true"
  consensus_threshold: 3         # confirmations for auto-promote (default: 3)
  grounding_threshold: 0.8       # min grounding score (default: 0.8)
  similarity_threshold: 0.85     # question matching threshold (default: 0.85)
  auto_promote: true             # auto-promote when all thresholds met (default: true)
```

### Trust Modes

| Mode | Behavior |
|------|----------|
| `"false"` | Outputs excluded from search entirely. Files still written to `wiki/under_review/` for human reference. New outputs enter the trust pipeline. |
| `"verified"` | Only confirmed (promoted) outputs appear in search results. Pending, stale, and conflict outputs are filtered out. |
| `"true"` | Legacy mode. All outputs are indexed immediately, identical to pre-trust behavior. No quarantine. |

The default is `"false"` -- the safest option. Set to `"verified"` when
you want trusted outputs to contribute to future queries.

### Thresholds

**consensus_threshold** -- How many independent confirmations are needed
before auto-promotion. A confirmation occurs when the same question is
asked again and produces the same answer. Independence is measured via
Jaccard distance between the chunk sets used by each confirmation.

**grounding_threshold** -- Minimum grounding score from `sage-wiki verify`.
Grounding checks extract factual claims from the answer and verify each
against the source passages via LLM entailment. Score is the fraction of
claims that are grounded. 0.8 means 80% of claims must be directly
supported by sources.

**similarity_threshold** -- Cosine similarity threshold for matching
questions. When a new query's question embedding is above this threshold
compared to an existing pending output, they are considered the "same
question." Lower values match more broadly.

**auto_promote** -- When true, outputs are automatically promoted when
both grounding and consensus thresholds are met. When false, promotion
is manual only (`sage-wiki outputs promote`).

## Trust States

Each output progresses through these states:

```
  [new query]
       |
       v
   PENDING -----> CONFIRMED (promoted, indexed)
       |               |
       v               v
   CONFLICT        STALE (source changed, demoted)
       |
       v
   [resolve]
```

| State | Meaning |
|-------|---------|
| **pending** | New output, awaiting verification and consensus |
| **confirmed** | Passed grounding and consensus, indexed in search |
| **conflict** | Same question produced contradictory answers |
| **stale** | Previously confirmed, but source files changed since |

## How the Pipeline Works

### 1. Query Creates Pending Output

When you run `sage-wiki query "question"` with trust mode enabled:

1. The LLM synthesizes an answer from wiki context
2. The question is embedded and compared against existing pending outputs
3. If no similar question exists: a new pending output is created in
   `wiki/under_review/` with state frontmatter
4. If a similar question exists and answers agree: confirmation count
   is incremented (idempotent -- same source chunks don't count twice)
5. If a similar question exists and answers disagree: both are marked
   as conflicts

### 2. Grounding Verification

Run `sage-wiki verify` to check pending outputs:

```bash
sage-wiki verify --all              # verify all pending
sage-wiki verify --since 7d         # only recent outputs
sage-wiki verify --limit 5          # cap LLM calls
sage-wiki verify --question "..."   # specific question
```

For each pending output, the verifier:

1. Extracts factual claims from the answer (LLM call)
2. For each claim, checks entailment against source passages (LLM call)
3. Computes a grounding score (fraction of grounded claims)
4. If score >= threshold AND consensus met: auto-promotes

**Cost:** Each output requires N+1 LLM calls (1 extraction + N
entailment checks, where N is the number of claims). Use `--limit`
to cap the number of outputs verified per run.

### 3. Consensus Building

Consensus builds naturally through repeated queries. When the same
question is asked multiple times:

- The answer is compared via embedding similarity (>=0.9 agree,
  <0.7 disagree, marginal triggers LLM comparison)
- Source chunks used by each query are tracked
- Independence is scored via Jaccard distance -- overlapping chunks
  provide less independent evidence than disjoint chunks

Auto-promotion requires:
- `confirmations >= threshold` AND `independence > 0.3`
- OR `confirmations >= 2 * threshold` (enough volume overcomes
  low independence)

### 4. Source Change Demotion

During `sage-wiki compile`, confirmed outputs are checked:

1. For each confirmed output, the hash of its cited source files is
   recomputed
2. If the hash differs from the stored `sources_hash`, the output is
   demoted to `stale`
3. Demoted outputs are de-indexed from FTS5, vectors, and ontology
4. They must be re-verified to regain confirmed status

This ensures that when you update a source document, any outputs that
cited it are flagged for re-verification.

### 5. Promotion and File Lifecycle

When an output is promoted (manually or automatically):

1. File moves from `wiki/under_review/` to `wiki/outputs/`
2. Indexed into FTS5 full-text search
3. Vector embedding stored for semantic search
4. Ontology entity created with `derived_from` edges to source concepts
5. Chunk-level index created for enhanced search
6. DB state updated to `confirmed` (last step, after all artifacts)

Demotion reverses steps 2-6 (de-indexes) and updates state to `stale`.

## Commands Reference

### sage-wiki verify

```bash
sage-wiki verify [--all] [--since <duration>] [--question "..."] [--limit 20]
```

Runs grounding checks on pending outputs. Output shows each output with
its grounding score and whether it was promoted.

### sage-wiki outputs list

```bash
sage-wiki outputs list [--state pending|confirmed|conflict|stale]
```

Lists outputs with their trust state, grounding score, confirmation
count, and question.

### sage-wiki outputs promote

```bash
sage-wiki outputs promote <id>
```

Manually promotes an output. Moves file to `outputs/`, indexes into all
search stores. Use when you've manually verified an output is correct.

### sage-wiki outputs reject

```bash
sage-wiki outputs reject <id>
```

Rejects an output. De-indexes from all stores, deletes the file, and
removes the trust record.

### sage-wiki outputs resolve

```bash
sage-wiki outputs resolve <id>
```

For conflicts: promotes the specified output and rejects all other
outputs for the same question. Use when you know which answer is correct.

### sage-wiki outputs clean

```bash
sage-wiki outputs clean [--older-than 90d]
```

Removes pending and stale outputs older than the specified duration.
Default: 90 days.

### sage-wiki outputs migrate

```bash
sage-wiki outputs migrate
```

Migrates existing `wiki/outputs/` files into the trust system. Each file
becomes a pending output with sources parsed from its frontmatter.
Idempotent -- already-migrated files are skipped. Existing outputs are
de-indexed from the main search stores.

## Database Schema

The trust system adds three tables (Migration V6):

```sql
pending_outputs     -- one row per output, tracks state and metadata
confirmation_sources -- one row per confirmation, tracks chunks used
pending_questions_vec -- question embeddings for similarity matching
```

These tables are created automatically on first use. No manual migration
needed.

## Architecture

```
Query → ProcessOutput → [embed question]
                            |
                     FindSimilarQuestion
                       /            \
                 [no match]      [match found]
                      |              |
              InsertPending    CompareAnswers
                      |          /        \
                      |     [agree]    [disagree]
                      |        |           |
                      |   RecordConfirm  SetConflict
                      |        |
                      |   CheckAutoPromote
                      |        |
                      v        v
              wiki/under_review/...

Verify → ExtractClaims → CheckEntailment → ComputeGroundingScore
                                               |
                                        [score >= threshold]
                                               |
                                        CheckConsensus → PromoteOutput
                                                              |
                                                    [move to outputs/]
                                                    [index FTS5/vec/ont]
                                                    [mark confirmed]

Compile → CheckSourceChanges → [hash mismatch] → DemoteOutput
                                                      |
                                              [de-index + mark stale]
```

## Troubleshooting

### Outputs not appearing in search

Check your trust mode:

```bash
grep include_outputs config.yaml
```

- `"false"`: outputs are never in search (by design)
- `"verified"`: only confirmed outputs appear -- run `sage-wiki outputs list`
  to see if any are confirmed
- `"true"`: all outputs indexed (legacy mode)

### Verify promotes nothing

Auto-promote requires BOTH grounding AND consensus:

```bash
sage-wiki outputs list --state pending
```

If outputs have 0 confirmations, they need more queries to build
consensus, or you can manually promote them.

### Too many conflicts

Conflicts occur when the same question produces different answers. This
can happen when source content is ambiguous or when the LLM is
non-deterministic. Use `sage-wiki outputs resolve <id>` to pick the
correct answer, or adjust `similarity_threshold` to be more strict
(higher value = fewer matches = fewer conflicts).

### Migration from legacy mode

If you've been running without trust and want to enable it:

1. Set `trust.include_outputs: verified` in config.yaml
2. Run `sage-wiki outputs migrate` to move existing outputs to pending
3. Run `sage-wiki verify --all` to grade them
4. Manually promote any that you know are correct

Existing search indexes remain intact until outputs are explicitly
de-indexed via reject or demote.
