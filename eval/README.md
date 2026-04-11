# sage-wiki eval

Performance benchmarks and quality evaluation for any sage-wiki project. The eval suite is **read-only** — it never modifies your wiki or database.

## Quick start

```bash
# From repo root — evaluate the current project
python3 eval/eval.py .

# Evaluate a specific project
python3 eval/eval.py ~/my-wiki

# Performance benchmarks only
python3 eval/eval.py --perf-only .

# Quality metrics only
python3 eval/eval.py --quality-only .

# Machine-readable JSON output
python3 eval/eval.py --json . > report.json
```

## Requirements

- **Python 3.10+**
- **numpy** (optional, speeds up vector benchmarks ~10x)
- A compiled sage-wiki project (must have `.sage/wiki.db` and `_wiki/` directory)

```bash
pip install numpy  # optional but recommended
```

No other dependencies — the eval uses only the Python standard library and SQLite.

## What it measures

### Performance benchmarks (`--perf-only`)

| Benchmark | What it tests |
|-----------|---------------|
| FTS5 BM25 search | Keyword search latency at top-1 and top-10, plus prefix search |
| Vector similarity | Brute-force cosine similarity search across all embeddings |
| Hybrid RRF | Combined BM25 + vector search with Reciprocal Rank Fusion |
| Graph traversal | BFS to depth 5 on the ontology graph |
| Cycle detection | Full DFS cycle detection on the ontology graph |
| Write throughput | FTS5 and vector insert rates at various batch sizes |
| Sustained load | Mixed read operations over 3 seconds (FTS + entity + neighbor queries) |
| File I/O | Read and SHA-256 hash all wiki files |

Each benchmark reports **p50, p95, avg, and QPS** (queries per second).

### Quality evaluation (`--quality-only`)

| Metric | What it measures |
|--------|-----------------|
| **Fact extraction density** | How many summaries contain structured claims (`## Key claims` sections) |
| **Concept coverage** | What fraction of concepts mentioned in summaries have their own article |
| **Cross-reference integrity** | Percentage of `[[wikilinks]]` that point to existing articles |
| **Search recall@1/5/10** | Can FTS5 find the correct article when you search by concept name? |
| **Source citation rate** | Percentage of concept articles that cite their source documents |
| **Ontology health** | Orphan entities, relation type distribution, average degree |
| **Content depth** | Word counts, structural completeness (expected sections present) |
| **Deduplication** | Near-duplicate concept detection (>85% name similarity), alias coverage |
| **Wiki connectivity** | Inbound link distribution, orphan concepts, hub identification |

The quality evaluation produces a **scorecard** with an overall score (0–100%).

## Output formats

### Human-readable (default)

```
sage-wiki eval
────────────────────────────────────────────────────────────────────────────────
  Project:    /home/user/my-wiki
  DB:         12.3 MB
  Sources:    147
  FTS entries:1,204
  Vectors:    1,204 × 3072d
  ...

────────────────────────────────────────────────────────────────────────────────
  Performance: FTS5 BM25 search
────────────────────────────────────────────────────────────────────────────────
  FTS MATCH top-1 (200 queries).................. p50=  411µs  p95=  812µs  avg=  564µs  qps=1,775

  ...

────────────────────────────────────────────────────────────────────────────────
  QUALITY SCORECARD
────────────────────────────────────────────────────────────────────────────────

  Fact extraction rate........................... █████████████████████░░░░░░░░░  68.5%
  Search recall@10.............................. ██████████████████████████████ 100.0%
  ...

  OVERALL                                                                       73.0%
```

### JSON (`--json`)

```bash
python3 eval/eval.py --json . > report.json
```

Top-level structure:

```json
{
  "dataset": {
    "path": "/home/user/my-wiki",
    "db_size_mb": 12.3,
    "sources": 147,
    "fts_entries": 1204,
    "vectors": 1204,
    "vec_dims": 3072,
    "entities": 892,
    "relations": 1456,
    "summary_files": 147,
    "concept_files": 1057
  },
  "performance": {
    "fts_top-1": { "p50": 0.411, "p95": 0.812, "avg": 0.564, "qps": 1775 },
    "vec_top10": { "p50": 81.2, "avg": 83.1, "qps": 15 },
    "hybrid_rrf": { "p50": 80.0, "avg": 82.5, "qps": 16 },
    "bfs": { "p50": 0.001, "qps": 738000 },
    "sustained": { "throughput": 8500, "ops": 25500 }
  },
  "quality": {
    "fact_extraction": { "rate": 0.685, "total_claims": 2875 },
    "search_recall": { "recall_at_10_rate": 1.0, "recall_at_1_rate": 0.916 },
    "scorecard": { "Fact extraction rate": 0.685, "Search recall@10": 1.0 },
    "overall_score": 0.73
  }
}
```

Use this to track quality over time, compare different LLM providers, or integrate into CI.

## Running tests

The test suite generates synthetic sage-wiki projects with known properties and validates eval.py against them.

```bash
# Run all tests
python3 -m pytest eval/eval_test.py -v

# Or with unittest directly
python3 eval/eval_test.py -v

# Run a specific test class
python3 eval/eval_test.py -k TestEvalRuns

# Generate a standalone fixture for manual testing
python3 eval/eval_test.py --generate-fixture ./test-fixture
python3 eval/eval.py ./test-fixture
```

### Test structure

| Test class | What it validates |
|------------|-------------------|
| `TestEvalFixture` | Fixture generation: directories, DB tables, entry counts |
| `TestEvalRuns` | Full/perf-only/quality-only/JSON execution paths |
| `TestEvalSearchRecall` | Search recall and citation metrics against known data |
| `TestEvalEdgeCases` | Nonexistent paths, small wikis, large fixtures (300 concepts) |

The fixture generator (`generate_fixture()`) creates a complete synthetic project with configurable parameters:

```python
generate_fixture(
    path,
    n_sources=50,       # source documents
    n_concepts=80,      # concept articles with wikilinks, aliases, frontmatter
    n_summaries=50,     # summary articles with claims sections
    n_vectors=40,       # vector embeddings (128d default)
    n_entities=100,     # ontology entities
    n_relations=120,    # ontology relations
    broken_link_ratio=0.3,  # fraction of wikilinks that are intentionally broken
    alias_ratio=0.8,    # fraction of concepts with aliases
    seed=42,            # reproducible randomness
)
```

## Reproducing the eval on your data

### Step 1: Compile your wiki

```bash
sage-wiki init
# Edit config.yaml — add your API key and choose an LLM
sage-wiki compile
```

### Step 2: Verify the project is ready

```bash
sage-wiki status
```

You need at minimum:
- `.sage/wiki.db` — the SQLite database with FTS5 and vector indexes
- `_wiki/concepts/` — generated concept articles
- `_wiki/summaries/` — generated source summaries
- `.manifest.json` — source manifest

### Step 3: Run the eval

```bash
# Full evaluation
python3 eval/eval.py /path/to/your/wiki

# Save results for comparison
python3 eval/eval.py --json /path/to/your/wiki > eval-$(date +%Y%m%d).json
```

### Step 4: Compare across runs

```bash
# After changing LLM, config, or sources — run again and diff
python3 eval/eval.py --json /path/to/your/wiki > eval-after.json

# Compare with jq
diff <(jq '.quality.scorecard' eval-before.json) <(jq '.quality.scorecard' eval-after.json)
```

## Interpreting results

### Quality scorecard

| Score | Meaning |
|-------|---------|
| 90%+ | Excellent — wiki is well-connected with high coverage |
| 70–90% | Good — typical for a mature wiki with 100+ sources |
| 50–70% | Fair — common for early-stage wikis or niche domains |
| <50% | Needs attention — check broken links and coverage gaps |

### Key metrics to watch

- **Search recall@10 < 90%** — FTS5 indexing may have issues, or concept names don't match article content
- **Cross-reference integrity < 50%** — many broken wikilinks; consider running `sage-wiki compile` to regenerate
- **Source citation rate < 80%** — concepts are being generated without tracing back to sources
- **Wiki connectivity < 50%** — too many orphan concepts; the LLM may not be creating enough cross-references

### Performance baselines

These are typical numbers on a wiki with ~1000 concepts and ~3000 chunks:

| Operation | Expected |
|-----------|----------|
| FTS5 search (top-10) | < 1ms |
| Vector search (brute-force) | 50–150ms depending on embedding dimensions |
| Hybrid RRF | ~same as vector search (vector dominates) |
| Graph BFS depth-5 | < 10µs |
| Sustained mixed reads | 5,000+ ops/s |

## Project structure

```
eval/
├── README.md          # this file
├── eval.py            # main eval script (perf + quality)
└── eval_test.py       # test suite with synthetic fixture generator
```

## Tips

- Install `numpy` for ~10x faster vector benchmarks
- Use `--json` output to track quality regressions in CI
- The eval is read-only and safe to run on production wikis
- For large wikis (10k+ articles), the full eval takes ~30 seconds; use `--perf-only` or `--quality-only` to speed things up
- The fixture generator is useful for testing sage-wiki itself — not just the eval
