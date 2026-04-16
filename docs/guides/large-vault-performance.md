# Scaling sage-wiki to Large Vaults

This guide covers how to configure and operate sage-wiki with vaults of
10K-100K+ documents. The key shift: instead of compiling every source
through the full LLM pipeline, sage-wiki **indexes everything fast** and
**compiles only what matters**.

## The Problem

A single document takes ~8 minutes to fully compile (summarize, extract
concepts, write articles). At 100K documents with `max_parallel=4`:

| Strategy | Time | Feasible? |
|----------|------|-----------|
| Serial | 555 days | No |
| max_parallel=4 | 139 days | No |
| max_parallel=50 | 11 days | Painful |
| **Tiered compilation** | **~5.5 hours to searchable** | **Yes** |

## Tiered Compilation

Every source file is assigned a tier that determines how much processing
it receives:

### Tier 0: Index Only (~5ms/doc, free)

FTS5 full-text indexing. The source content is searchable via keyword
search. No LLM calls, no embeddings. Best for structured data that
would produce garbage articles (JSON, YAML, lock files, config).

### Tier 1: Index + Embed (~200ms/doc, ~$0.00002)

FTS5 indexing + vector embedding. Enables both keyword and semantic
search. The default tier for most content. For code files, structural
parsing is also run at this tier (see Code Parsers below).

### Tier 2: Code Parse (~10ms/doc, free)

Code files get structural summaries via regex parsers: imports, exports,
types, functions, signatures. This replaces LLM summarization for code
— deterministic, no hallucination, and free. Not applicable to prose.

### Tier 3: Full Compile (~5-8 min/doc, ~$0.05-0.15)

The full LLM pipeline: summarize, extract concepts, write articles, build
ontology. Only sources that matter — either promoted via usage signals
or explicitly configured — go through this expensive process.

## Configuration

### Basic Setup

For most large vaults, the defaults work well:

```yaml
compiler:
  max_parallel: 20         # adaptive backpressure handles rate limits
  mode: auto               # batch API when 10+ sources (50% cost savings)
  default_tier: 1          # override: index + embed only (global default is 3)
```

> **Note:** The global default is `default_tier: 3` (full compile). For large
> vaults (10K+), override to `1` here to index fast and compile on demand.

### File-Type Tier Overrides

Map file extensions to default tiers. Saves time and money by keeping
structured data out of the LLM pipeline:

```yaml
compiler:
  tier_defaults:
    # Structured data — never compile
    json: 0
    yaml: 0
    toml: 0
    lock: 0
    # Prose — index + embed (default)
    md: 1
    txt: 1
    pdf: 1
    # Code — index + embed + parse
    go: 1
    ts: 1
    py: 1
    rs: 1
    # Always compile these
    # (use .wikitier or frontmatter for per-file overrides)
```

### Per-File and Per-Directory Overrides

Create a `.wikitier` file in any source directory:

```yaml
# raw/important/.wikitier
"*.md": 3              # always compile markdown in this folder
"README.md": 0         # except READMEs
"*.json": 0            # never compile JSON
```

Or add `tier: 3` to a source file's YAML frontmatter:

```yaml
---
title: Critical Architecture Doc
tier: 3
---
```

**Priority order:** frontmatter > `.wikitier` > `tier_defaults` > `default_tier`

### Auto-Promotion and Demotion

Sources promote to higher tiers based on usage:

```yaml
compiler:
  auto_promote: true           # default
  promote_signals:
    query_hit_count: 3         # promote after 3 search hits
    cluster_size: 5            # promote when 5+ sources on same topic
    manual_tag: "compile"      # promote if frontmatter has this tag
    import_centrality: 10      # code: promote when 10+ files import this
    source_recency_days: 7     # boost recently modified sources

  auto_demote: true            # default
  demote_signals:
    source_modified: true      # revert to Tier 1 when source changes
    stale_days: 90             # demote if no queries in 90 days
```

When a source is modified, it demotes to Tier 1 automatically. Its
existing articles are marked as stale (not deleted). On the next access
via compile-on-demand, it recompiles with updated content.

## Compile-on-Demand

The wiki grows organically around topics you actually query. When you
search for a topic, the response signals uncompiled sources:

```json
{
  "results": [...],
  "uncompiled_sources": 8,
  "compile_hint": "Found 8 matching sources. Use wiki_compile_topic(\"flash attention\") to compile."
}
```

### MCP Tool

Any MCP client (Claude Code, Cursor, etc.) can trigger compilation:

```
wiki_compile_topic("flash attention", max_sources=20)
```

This:
1. Searches for sources matching the topic
2. Filters to uncompiled sources (Tier < 3)
3. Promotes them to Tier 3
4. Runs the full pipeline (~2 min for 20 sources)
5. Returns new articles

### How It Works

After a few weeks of use, the most important 1-5% of your vault is fully
compiled with rich ontology and articles. The rest is indexed and
searchable via Tier 1. Zero upfront compilation cost for initial ingest.

## Adaptive Backpressure

sage-wiki automatically adjusts concurrency to your provider's rate limits:

- **Starts at `max_parallel`** (default 20)
- **On 429 (rate limit):** halves concurrency, exponential backoff with jitter
- **On success streak (5 consecutive):** doubles concurrency back up
- **Self-tuning:** adapts to any provider's limits at runtime

No manual tuning needed. If you're on a free tier with low limits, the
system converges to the right concurrency within a few requests.

```yaml
compiler:
  max_parallel: 20         # starting point (safe for all paid tiers)
  backpressure: true       # default — disable with false
```

## Code Parsers

sage-wiki includes 10 built-in code parsers that extract structural
information without LLM calls:

| Language | Parser | What's Extracted |
|----------|--------|-----------------|
| Go | `go/parser` (stdlib) | Package, imports, types, fields, methods, functions (perfect accuracy) |
| TypeScript/JavaScript | Regex | Imports, exports, classes, interfaces, types, enums, functions |
| Python | Regex | Imports, classes, functions (module + method level), decorators |
| Rust | Regex | use, pub fn, struct, enum, trait, impl, mod |
| Java | Regex | Imports, classes, interfaces, enums, methods, annotations |
| C/C++ | Regex | #include, structs, classes, typedefs, functions, namespaces |
| Ruby | Regex | require, class, module, def, attr_accessor |
| JSON | stdlib | Top-level + nested keys (depth 3) |
| YAML | stdlib | Top-level + nested keys (depth 3) |
| TOML | Regex | Sections + keys |

Structural summaries are appended to FTS5 entries, making code
searchable by function name, type, import path, etc.

**Graceful degradation:** Regex parsers target ~90% coverage. Complex
syntax (TypeScript generics, Python decorator chains) may be missed —
but missing symbols are caught at Tier 3 when the LLM processes the
full source.

## Document Splitting

For large documents (>15K characters), sage-wiki splits the source at
heading boundaries before the article writing pass. This reduces the
context per LLM call by 3-4x, enabling more parallelism:

```yaml
compiler:
  split_threshold: 15000      # characters — split above this
  split_strategy: headings    # split at markdown headings
```

The summarize pass still sees the full document (for cross-section
awareness). Only the write pass receives targeted sections.

## Concept Deduplication

At scale (100K documents, 300K+ concepts), duplicates accumulate. An
embedding-based dedup cache catches near-duplicates before article writing:

```yaml
compiler:
  dedup_threshold: 0.85       # cosine similarity for auto-merge
  dedup_strategy: embedding   # or "llm" (always use LLM for dedup)
```

When a new concept like "scaled dot-product attention" has 0.92 cosine
similarity with existing "self-attention", it merges as an alias instead
of creating a duplicate article. This saves one LLM call per dedup hit.

The cache loads existing concept embeddings from the vector store (no
API calls for seeded concepts) and caps at 50K entries (~75MB).

## Quality Scoring

Every compiled article gets a quality score based on verifiable signals:

- **Source coverage (40%):** What fraction of source key phrases appear in the article
- **Extraction completeness (30%):** Fraction of concepts that have articles
- **Cross-reference density (30%):** Fraction of concepts with ontology relations

Articles below 0.5 are flagged in `sage-wiki lint` (quality pass).
The score is stored in `compile_items.quality_score` and reported in
`sage-wiki status`.

## Session Scribe

Extract entities from Claude Code session transcripts into your wiki:

```bash
sage-wiki scribe path/to/session.jsonl
```

The scribe:
1. **Compresses** the session (strips thinking blocks, tool results — ~99% reduction)
2. **Extracts** entity candidates via LLM (max 10/session, kebab-case ID required)
3. **Compares** against existing entities (ADD / UPDATE / skip duplicates)

Entities and relations are added to the ontology. Use compile-on-demand
to generate articles from scribe-created entities.

See the [evaluation plan](../../.sage/docs/evaluation-session-scribe.md)
for precision targets and known limitations.

## Cost Estimation

At `default_tier: 1` with cloud embeddings:

| Vault Size | Tier 0+1 Time | Tier 0+1 Cost | Full Compile (Tier 3) |
|-----------|--------------|--------------|----------------------|
| 1K docs | ~3 minutes | ~$0.02 | ~$50-150 (all) |
| 10K docs | ~30 minutes | ~$0.20 | ~$500-1500 (all) |
| 100K docs | ~5.5 hours | ~$2.00 | ~$5K-15K (all, not recommended) |
| 100K docs | ~5.5 hours | ~$2.00 | ~$50-150 (top 1% via on-demand) |

With local embeddings (Ollama), Tier 0+1 cost is zero.

## Performance Targets

| Operation | Target |
|-----------|--------|
| 100K sources to Tier 0 (searchable) | ~8 minutes |
| 100K sources to Tier 1 (semantic search) | ~5.5 hours |
| 20-source on-demand compile | ~3 minutes |
| Search with uncompiled source signaling | <100ms overhead |
| Backpressure recovery after rate limit | <5 seconds |

## Monitoring

### Status Command

```bash
sage-wiki status
```

Shows tier distribution, compilation progress, quality metrics:

```
Project: my-research (greenfield)
Sources: 1903 (0 pending)
Concepts: 628
Tiers: T0(index)=952 T1(embed)=901 T3(compile)=50
Compiled: 50 fully, 3 with errors, avg quality 0.72
```

### Lint Quality Pass

```bash
sage-wiki lint --pass quality
```

Reports low-quality articles, tier distribution, and compilation errors.

## Recommended Workflow for Large Vaults

1. **Initial ingest:** Run `sage-wiki compile` with `default_tier: 1`. All sources get indexed + embedded. Takes hours, not months.

2. **Explore via search:** Use `sage-wiki search` or MCP `wiki_search`. Results include uncompiled source hints.

3. **Compile on demand:** When search reveals relevant uncompiled sources, call `wiki_compile_topic("topic")` via MCP. Articles appear in ~2 minutes.

4. **Let promotion work:** After a few weeks, the most-queried sources auto-promote to Tier 3. The wiki grows around your actual interests.

5. **Monitor quality:** Run `sage-wiki lint` periodically. Low-quality articles surface for review. Stale articles auto-demote.

6. **Capture from conversations:** Use `sage-wiki scribe` to extract entities from session transcripts. The wiki becomes a convergence layer for all knowledge, not just documents.

## Further Reading

- [Local Model Configuration](local-models.md) — per-pass model routing, GPU/CPU/mixed setups
- [Search Quality](search-quality.md) — chunk-level indexing, query expansion, re-ranking
- [Configurable Relations](configurable-relations.md) — custom ontology types
- [Self-Hosted Server](self-hosted-server.md) — Docker, Syncthing, reverse proxy
- [MCP Knowledge Capture](mcp-knowledge-capture.md) — capturing from AI conversations
