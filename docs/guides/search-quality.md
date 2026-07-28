# Enhanced Search & Query Quality

sage-wiki's search pipeline fuses document- and chunk-level lexical and vector
retrieval with the ontology graph, then optionally expands and re-ranks with an
LLM. This guide covers how it works and every knob that shapes it.

## How it works

The enhanced pipeline replaces the document-level search with a multi-stage process:

```
User Query
  -> Strong-signal probe (fast BM25, skip expansion if confident)
  -> Query expansion (LLM: keyword + semantic + hypothetical answer variants)
  -> Parallel legs, per query variant:
     +-- BM25 chunk-level FTS5   +  BM25 document-level FTS5
     +-- Vector chunk-level      +  Vector document-level (cosine)
     +-- Graph proximity (ontology, <=2 hops, per-relation weights)
  -> Weighted RRF fusion, document-keyed: w_channel / (60 + rank)
  -> Hydration (legs that returned an ID but no text fetch it)
  -> LLM re-ranking (top-15 candidates scored for relevance)
  -> Normalized [0,1] blending (retrieval + rerank), behind a coverage gate
  -> Tag filter/boost -> recency tie-breaker -> trust rule -> limit
  -> [Q&A only] graph-expanded context, article reads, token budget, synthesis
```

Fusion is document-keyed from the start: a document hit by several legs sums
their contributions, so agreement across granularities (the same document
matching both as a whole and in one passage) ranks above a single strong hit.
Every channel weight comes from config, and a query can narrow the channel set
per call (see **Per-call ablation** below).

The document-level-only pipeline remains available as `search.pipeline: legacy`
for rollback. Graph *context expansion* for Q&A works on both paths; the graph
*ranking channel* is part of the unified pipeline only.

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

### Cross-lingual vector search

The enhanced pipeline runs full brute-force cosine search across all chunk vectors, independent of BM25 results. This ensures multilingual queries (e.g., Polish query against English content) find semantically relevant results even when there's zero lexical overlap between the query and content languages. BM25 and vector results are combined via RRF fusion, so both keyword and semantic matches contribute to the final ranking.

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

Seed entities that entity resolution has linked as aliases are resolved to
their canonical entity first, so a hit on an alias expands from the whole
cluster's neighborhood. For direct graph questions, the `wiki_graph_query`
MCP tool answers only from a bounded, serialized set of edges — citations
carry source-document provenance when the edge is evidenced — see
[graph-memory.md](graph-memory.md#asking-the-graph).

### Token budget control

The query context is capped at a configurable token limit (default 8000). Articles are prioritized by combined score (RRF + graph relevance) and filled greedily:

1. Primary search results first (from hybrid search)
2. Graph-expanded articles next (sorted by relevance score)
3. Depth-1 ontology traversal last (fallback for articles not yet included)

Each article is truncated at 4000 tokens (16000 chars, using chars/4 estimation). When the budget is exhausted, remaining articles are skipped.

### In-memory vector cache

Vector search runs over a per-process in-memory matrix (normalized at
load), ~11× faster than per-query SQLite scans at 10K+ chunks. Writes from
the same process keep it coherent. **Caveat:** a long-lived MCP/web server
does not observe vector writes made by a *separate CLI process* until
restart — restart the server after bulk out-of-process writes (e.g.
`sage-wiki write` or a batch `compile` against a project the server is
watching).

### ANN vector search (opt-in)

For very large vaults, enable approximate nearest-neighbor search (HNSW,
pure Go) — brute-force exact search stays the default:

```yaml
search:
  ann:
    enabled: true
```

## Configuration

All retrieval features are enabled by default with zero config. The two **LLM
stages are the exception**: `sage-wiki query` runs expansion and re-ranking by
default, while the search surfaces (MCP `wiki_search`, `sage-wiki search`,
`/api/search`, the TUI) leave them **off** and opt in per call — they cost an
LLM round trip per query, which a search call should not spend without being
asked. `search.query_expansion` and `search.rerank` therefore govern
`sage-wiki query` only.

Add these to `config.yaml` to customize:

```yaml
search:
  hybrid_weight_bm25: 0.7     # lexical channel weight in the fused ranking (all surfaces)
  hybrid_weight_vector: 0.3   # vector channel weight
  hybrid_weight_graph: 0.2    # ontology-graph channel weight
  graph_relation_weights:     # per-relation traversal weights (0 excludes a relation)
    contradicts: 1.1          # built-in defaults: contradicts 1.1, cites 0.7, others 1.0
  pipeline: unified           # "unified" (default) or "legacy" (doc-level rollback)
  default_limit: 10
  query_expansion: true       # LLM query expansion (sage-wiki query only; default: true)
  rerank: true                # LLM re-ranking (sage-wiki query only; default: true)
  rerank_min_coverage: 0.5    # 0.0-1.0 — min fraction of candidates the LLM must score
                              # for the rerank blend to apply; below it, RRF order stands
  chunk_size: 800             # tokens per chunk for indexing (100-5000, default: 800)
  chunk_overlap_tokens: 0     # tokens repeated from the previous chunk (default: 0; recommended opt-in: 80)
  graph_expansion: true       # graph-based context expansion (default: true)
  graph_max_expand: 10        # max articles added via graph
  graph_depth: 2              # ontology traversal depth (1-5)
  context_max_tokens: 8000    # token budget for query context
  weight_direct_link: 3.0     # graph signal weights (all configurable)
  weight_source_overlap: 4.0
  weight_common_neighbor: 1.5
  weight_type_affinity: 1.0
```

### Disabling the Q&A LLM stages

These affect `sage-wiki query` only — the search surfaces never run the LLM
stages unless a call asks for them.

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

### Chunk overlap

A fact that straddles a chunk boundary is easy to lose: neither chunk
contains the whole of it, so neither ranks for a query that asks about it.
`chunk_overlap_tokens` repeats the tail of each chunk at the head of the next,
so boundary-straddling facts are retrievable from either side.

```yaml
search:
  chunk_size: 800
  chunk_overlap_tokens: 80   # ~10% of the chunk — the recommended opt-in
```

The default is **0** (no overlap — byte-identical to previous versions, so
upgrading never re-chunks an existing index). The maximum is half of
`chunk_size`; beyond that a chunk is mostly duplicated text, which grows the
index without adding recall.

**Changing this value requires a reindex, and the two are one step:**

```bash
# 1. edit config.yaml   2. rebuild the chunk index
sage-wiki reindex
```

`reindex` re-chunks every document that carries chunks — compiled articles in
`concepts/`, `summaries/` and `outputs/`, plus raw sources indexed as
`src:<path>` — with the current `chunk_size` and `chunk_overlap_tokens`,
replacing the chunk FTS rows and chunk vectors. No LLM article writing
happens; the only API cost is re-embedding the new chunks.

Re-chunking changes chunk IDs, so the old chunk vectors cannot be carried
over. Without a working embedding provider the command stops rather than
leave the chunk-vector leg empty; `--drop-chunk-vectors` rebuilds the text
index anyway and leaves chunk-level vector search off until the next
`sage-wiki compile --re-embed`.

Compiling normally after a config change does NOT re-chunk documents that did
not change — the index would mix the old and new chunkings until every
document happened to be rewritten. Change the value and reindex together.

### Recency

A document with a known origin date receives a small ranking bonus:

```
bonus = 0.05 x 2^(-age_days / 14)
```

At most 5% of a normalized score, halving every two weeks — a tie-breaker
between comparably relevant documents, never a driver. Undated documents get
nothing at all: there is no fallback timestamp, because a made-up date would
rank a document it knows nothing about above one it does.

The origin date is resolved **once, at index time**, in this order:

1. the source's YAML frontmatter `date:` (`date: 2024-03-01`, or any RFC-3339
   / `2006-01-02` / `2006/01/02` form — timezone optional),
2. the source file's modification time,
3. the manifest's first-seen time for that source.

So `date:` in a source's frontmatter is a **ranking input you control**: set it
when a file's mtime lies about the content's age (a re-exported note, a
restored backup, a bulk import). Q&A outputs stamp their creation time. Dates
land on both the compiled article and the raw-source entry, and are returned
with results (`SourceDate` in CLI/MCP JSON, `source_date` on the web API).

Existing vaults gain dates without a recompile: the `entry_dates` sidecar
backfills on the next compile, and until then those documents simply receive
no recency bonus.

### Per-call ablation

Every search surface can narrow the channel set for a single call, without
touching config — useful for diagnosing whether a bad ranking came from the
lexical, vector, or graph side:

```bash
sage-wiki search "quantum error correction" --tags physics           # hard filter: only tagged docs
sage-wiki search "quantum error correction" --boost-tags physics     # soft boost: tagged docs rank higher
sage-wiki search "quantum error correction" --channels bm25          # lexical only
sage-wiki search "quantum error correction" --channels bm25,vector   # no graph
sage-wiki search "quantum error correction" --expand --rerank        # opt into the LLM stages
```

`--tags` and `--boost-tags` are different tools: the filter excludes
everything untagged, while the boost adds +3% per matching tag (capped at
15%) and changes only the order. MCP `wiki_search` takes all of these as
tool arguments (`tags`, `boost_tags`, `channels`, `expand`, `rerank`). An unknown channel name is an error, not a silent
ignore. `--expand`/`--rerank` need a usable LLM client; if the call cannot
build one it fails rather than quietly searching without them.

Note that `hybrid_weight_graph: 0` does **not** disable the graph channel —
`0` means "use the default" for every weight key, since an omitted key is
also zero. Use `--channels bm25,vector` (or `channels` on MCP) to turn the
graph channel off for a call.

### Corpus-adaptive stopwording

Above **100 documents**, a query term that prefix-matches more than **20%** of
documents is dropped from the lexical query: on a large corpus such a term
carries no discriminating signal and only dilutes BM25. If every term would be
pruned, the first three are kept, so a query of nothing but common words still
returns something. Both the document and chunk legs prune on identical
document-ratio semantics, so the two legs never disagree about which terms
matter.

### Title-proxy column weights

Entry matching weights the `id` and `article_path` columns **3x** over body
content (tags 1.5x). A concept's slug and its article path are title proxies,
so a query naming a concept ranks that concept's own article above articles
that merely mention it. Postgres applies the same weighting through
`setweight` (A: id + path, B: tags, D: content).

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
| **Vector search** | Brute-force cosine (cross-lingual safe) | Brute-force |
| **Hybrid search** | Weighted RRF over 3 channels (BM25 + vector + ontology graph), at both document and chunk granularity | Vector-only |
| **Strong-signal skip** | Yes (normalized BM25 threshold) | No |
| **Graph context** | 4-signal expansion (relations, sources, neighbors, types) + 1-hop fallback | No graph |
| **Model dependency** | Any provider (cloud or local via Ollama) | Local GGUF models |
| **Cost per query** | Free (Ollama) / ~$0.0006 (cloud) | Free (local) |

Key differences:

- **sage-wiki uses three-channel retrieval** (BM25 + vector + ontology graph) at both document and chunk level, while qmd relies primarily on vector similarity. BM25 excels at exact keyword matches that vector search misses; the graph channel reaches documents connected to the query's entities even when neither lexical nor vector similarity finds them.
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

Schema migrations apply automatically on the first writer command after an
upgrade; no manual steps are needed.

- **Chunk tables** (SQLite `migrationV3`): added when chunk-level indexing
  shipped. On the first `sage-wiki compile` after upgrading, existing articles
  are chunk-indexed via backfill — once, and transparently.
- **Origin dates** (SQLite `migrationV13`, Postgres `v7`): adds the
  `entry_dates` sidecar behind the recency signal. Nothing is re-indexed;
  documents gain dates as they are next compiled, and undated documents simply
  receive no recency bonus.
- **Weighted search vector** (Postgres `v6`): rebuilds the generated `tsv`
  column with `setweight`, so Postgres ranks id/path above body content like
  SQLite does. This rewrites the `entries` table under an exclusive lock — on a
  large vault, run the first writer command in a maintenance window.

Changing `search.chunk_size` or `search.chunk_overlap_tokens` is the one
case that is **not** automatic: run `sage-wiki reindex` (see
[Chunk overlap](#chunk-overlap)).
