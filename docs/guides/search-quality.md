# Enhanced Search & Query Quality

sage-wiki v0.1.3 introduces an enhanced search pipeline with chunk-level indexing, LLM query expansion, and LLM re-ranking. These features significantly improve retrieval quality for natural language Q&A queries.

## How it works

The enhanced pipeline replaces the document-level search with a multi-stage process:

```
User Query
  -> Strong-signal probe (fast BM25, skip expansion if confident)
  -> Query expansion (LLM: keyword + semantic + hypothetical answer variants)
  -> Parallel search:
     +-- BM25 on original + keyword variants (chunk-level FTS5)
     +-- Vector search on semantic/hyde variants (chunk-level, BM25-prefiltered)
  -> RRF fusion (reciprocal rank fusion)
  -> Deduplicate to document level (best chunk per doc)
  -> LLM re-ranking (top-15 candidates scored for relevance)
  -> Position-aware blending (retrieval + rerank scores)
  -> Graph-expanded context (4-signal relevance: relations, sources, neighbors, types)
  -> Read full articles + ontology traversal (depth-1 fallback)
  -> Token budget control (greedy fill up to context_max_tokens)
  -> LLM synthesis
```

The original document-level pipeline remains as a fallback when chunk data is unavailable. Graph expansion works on both paths.

## Key features

### Chunk-level indexing

Articles are split into ~800-token chunks during compilation. Each chunk gets its own FTS5 entry and vector embedding. This means a search for "flash attention" can find the relevant paragraph inside a 3000-token article about Transformer Architecture, instead of relying on the whole-document embedding.

Chunks are indexed automatically during `sage-wiki compile`. On first compile after upgrading, existing articles are backfilled without requiring a full recompile.

### Query expansion

A single LLM call generates three types of search variants:

- **lex** (2 variants) — keyword-rich rewrites for BM25 (e.g., "flash attention" -> "flash attention GPU memory optimization", "attention SRAM tiling")
- **vec** (1 variant) — natural language rewrite for vector search
- **hyde** (1 variant) — hypothetical answer sentence for embedding similarity

A **strong-signal check** runs first: if the top BM25 result is already highly confident (normalized score >= 0.4 with 2x gap over #2), expansion is skipped entirely. This saves an LLM call on simple queries.

### LLM re-ranking

After retrieval, the top 15 candidates are sent to the LLM in a single call for relevance scoring. Each chunk is truncated to 400 tokens, with a total budget of 8000 tokens.

**Position-aware blending** protects high-confidence retrieval results from reranker noise:

| Retrieval rank | Retrieval weight | Reranker weight |
|---|---|---|
| 1-3 | 75% | 25% |
| 4-10 | 60% | 40% |
| 11+ | 40% | 60% |

This ensures that if RRF placed something at rank 1 with high confidence, the reranker can't easily demote it.

### BM25-prefiltered vector search

Instead of brute-force scanning all chunk vectors, the enhanced pipeline uses BM25 results as a candidate filter. Vector comparisons are limited to chunks from the top 50 BM25 documents, capping cosine computations at ~250 regardless of wiki size.

### Graph-enhanced context expansion

After retrieval, a 4-signal graph relevance scorer discovers related articles via the ontology that keyword/vector search may have missed:

| Signal | Weight | How it works |
|---|---|---|
| **Direct link** | 3.0 | Ontology relation exists between a search result and the candidate (excluding `cites` edges) |
| **Source overlap** | 4.0 | Both concepts were generated from the same source document (via shared `cites` edges in ontology) |
| **Common neighbor** | 1.5 | Adamic-Adar index: shared ontology neighbors weighted by `1/log(degree)` — common neighbors that are themselves highly connected contribute less |
| **Type affinity** | 1.0 | Bonus for cross-type pairs (e.g., concept↔technique scores 1.2, same-type scores 0.5-0.8) |

Source overlap is the highest-weighted signal because articles generated from the same document are inherently related — and this costs zero compute (just set intersection on existing ontology `cites` edges).

Graph expansion is applied **before** both the enhanced (chunk-level) and document-level search sub-functions, so both code paths benefit uniformly. Expanded articles are added to the context with `### Graph-related:` headers and tracked in `QueryResult.Sources` for provenance.

### Token budget control

The query context is capped at a configurable token limit (default 8000). Articles are prioritized by combined score (RRF + graph relevance) and filled greedily:

1. Primary search results first (from hybrid search)
2. Graph-expanded articles next (sorted by relevance score)
3. Depth-1 ontology traversal last (fallback for articles not yet included)

Each article is truncated at 4000 tokens (16000 chars, using chars/4 estimation). When the budget is exhausted, remaining articles are skipped.

## Configuration

All features are enabled by default with zero config. Add these to `config.yaml` to customize:

```yaml
search:
  hybrid_weight_bm25: 0.7    # BM25 vs vector weight (doc-level fallback)
  hybrid_weight_vector: 0.3
  default_limit: 10
  query_expansion: true       # LLM query expansion (default: true)
  rerank: true                # LLM re-ranking (default: true)
  chunk_size: 800             # tokens per chunk for indexing (100-5000, default: 800)
  graph_expansion: true       # graph-based context expansion (default: true)
  graph_max_expand: 10        # max articles added via graph
  graph_depth: 2              # ontology traversal depth (1-5)
  context_max_tokens: 8000    # token budget for query context
  weight_direct_link: 3.0     # graph signal weights (all configurable)
  weight_source_overlap: 4.0
  weight_common_neighbor: 1.5
  weight_type_affinity: 1.0
```

### Disabling features

```yaml
# Disable expansion (saves ~1 LLM call per query)
search:
  query_expansion: false

# Disable re-ranking (saves ~1 LLM call per query)
search:
  rerank: false

# Disable both (chunk-level BM25+vector search still active)
search:
  query_expansion: false
  rerank: false

# Disable graph expansion (uses only depth-1 ontology traversal)
search:
  graph_expansion: false
```

### Local models (Ollama)

When using Ollama as the LLM provider, re-ranking is automatically disabled by default. Local models often struggle with the structured JSON output that reranking requires. To force-enable it:

```yaml
api:
  provider: ollama
search:
  rerank: true    # explicitly enable for capable local models
```

Query expansion works well with most local models and remains enabled.

### Chunk size tuning

The default chunk size of 800 tokens works well for most content. Adjust if:

- **Shorter chunks (400-600):** Technical docs with dense, self-contained paragraphs
- **Longer chunks (1000-1500):** Narrative content where context spans multiple paragraphs
- **Maximum (5000):** Effectively disables chunking (one chunk per article)

```yaml
search:
  chunk_size: 600   # smaller chunks for technical docs
```

## Cost

**With local models (Ollama): free.** Chunk-level indexing and query expansion run locally at no cost. Re-ranking is auto-disabled for local models (see above), so the enhanced pipeline adds zero API cost. You still get chunk-level BM25+vector search and LLM query expansion — just no re-ranking.

**With cloud LLMs:** the enhanced pipeline adds two small LLM calls per Q&A query:

| Component | Tokens | Cost (Gemini Flash) |
|---|---|---|
| Query expansion | ~100 in, ~80 out | ~$0.0001 |
| Re-ranking | ~2000 in, ~200 out | ~$0.0005 |
| Extra embeddings | 3-4 vectors | ~$0.00003 |
| **Total per query** | | **~$0.0006** |

For context, that's less than $1 for 1,500 queries. The strong-signal optimization skips expansion entirely for simple keyword queries, further reducing cost. Both expansion and re-ranking can be disabled via config if needed.

## Comparison with qmd

sage-wiki's enhanced search pipeline was inspired by analyzing [qmd](https://github.com/dmayboroda/qmd)'s retrieval approach. Here's how they compare:

| Feature | sage-wiki | qmd |
|---|---|---|
| **Chunk indexing** | FTS5 + vector per chunk | Vector-only chunks |
| **Chunk size** | 800 tokens (configurable) | 900 tokens |
| **Query expansion** | LLM-based (lex/vec/hyde) | LLM-based |
| **Re-ranking** | LLM batch scoring + position-aware blending | Cross-encoder |
| **Vector search** | BM25-prefiltered (caps at ~250 comparisons) | Brute-force |
| **Hybrid search** | RRF fusion (BM25 + vector) | Vector-only |
| **Strong-signal skip** | Yes (normalized BM25 threshold) | No |
| **Graph context** | 4-signal expansion (relations, sources, neighbors, types) + 1-hop fallback | No graph |
| **Model dependency** | Any provider (cloud or local via Ollama) | Local GGUF models |
| **Cost per query** | Free (Ollama) / ~$0.0006 (cloud) | Free (local) |

Key differences:

- **sage-wiki uses dual-channel retrieval** (BM25 + vector) at both document and chunk level, while qmd relies primarily on vector similarity. BM25 excels at exact keyword matches that vector search misses.
- **sage-wiki's position-aware blending** protects high-confidence retrieval results from reranker noise, using different weight tiers based on pre-rerank position.
- **sage-wiki adds graph-enhanced context** — after search, a 4-signal scorer (direct relations, source overlap, Adamic-Adar neighbors, type affinity) finds structurally related articles and adds them to the LLM synthesis context. This goes beyond simple 1-hop traversal — it discovers concepts that share source documents or have common ontology neighbors.
- **Both support local models for free inference.** qmd uses GGUF via llama.cpp; sage-wiki supports Ollama (or any OpenAI-compatible local server). With Ollama, sage-wiki's enhanced search is completely free — chunk indexing, query expansion, and BM25+vector search all run locally. Re-ranking is auto-disabled for local models but can be force-enabled for capable ones. With cloud LLMs, the additional cost per query is negligible (~$0.0006).

## Fallback behavior

The enhanced pipeline degrades gracefully:

- **No chunks indexed yet** — Falls back to document-level search. Logs: "chunk index empty — using document-level search."
- **LLM expansion fails** — Uses the raw query without variants.
- **LLM reranking fails** — Uses RRF order as-is.
- **Graph expansion fails or empty ontology** — Falls back to depth-1 ontology traversal. Logged at debug level.
- **No embedder configured** — BM25-only search with expansion keywords.
- **Empty wiki** — Returns "no results" immediately.

## Migration

Upgrading to v0.1.3 adds chunk tables automatically (`migrationV3`). No manual steps needed. On the first `sage-wiki compile` after upgrading, existing articles are chunk-indexed via backfill — this runs once and is transparent.
