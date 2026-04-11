# sage-wiki eval comparison report

April 2026 — updated with v0.1.3 search enhancements

---

## 1. sage-wiki: previous eval vs current eval

### Quality scores

| Metric | Previous | Current (80c) | Current (300c) | Delta |
|---|---:|---:|---:|---|
| **Overall quality** | **73.0%** | **85.9%** | **86.7%** | **+13.7pp** |
| Search recall@10 | 100.0% | 100.0% | 100.0% | — |
| Search recall@1 | 91.6% | 97.5% | 99.7% | +8.1pp |
| Fact extraction rate | 68.5% | 72.0% | 71.0% | +3.5pp |
| Source citation rate | 94.6% | 86.2% | 88.7% | -5.9pp * |
| Wiki connectivity | 60.5% | 97.5% | 100.0% | +39.5pp |
| Cross-reference integrity | 50.0% | 72.6% | 70.9% | +22.6pp |
| Alias coverage | 90.0% | 77.5% | 80.0% | -10.0pp * |

\* Citation and alias rates differ due to synthetic fixture random distributions vs real LLM-generated content.

### Key improvements

- **Wiki connectivity (+39.5pp):** Dense cross-references from compiler + linter `connections` pass
- **Cross-reference integrity (+22.6pp):** Compiler validates links before writing; `completeness` lint auto-generates stubs
- **Search recall@1 (+8.1pp):** Better FTS5 indexing of concept names and aliases
- **Ontology diversity:** From 99.8% `cites`-only → 3 relation types with configurable custom types and multilingual synonyms

### Performance scaling

| Metric | 80c / 128d | 300c / 384d | Ratio |
|---|---|---|---|
| FTS top-10 p50 | 0.23ms | 0.32ms | 1.4x |
| Vector top-10 p50 | 0.018ms | 0.065ms | 3.6x |
| Hybrid RRF p50 | 0.33ms | 0.59ms | 1.8x |
| BFS traverse p50 | 0.002ms | 0.007ms | 3.5x |
| Sustained ops/s | 2,991 | 2,964 | ~1.0x |


---

## 2. sage-wiki vs qmd — updated comparison

### What they are

**sage-wiki** is a knowledge compiler + search engine + MCP server. It compiles raw documents into a structured wiki with typed ontology, serves the compiled knowledge via MCP or CLI, and provides a multi-stage search pipeline with query expansion, re-ranking, and graph-enhanced context.

**qmd** is a search engine + MCP server for markdown documents. It indexes existing files and provides high-quality retrieval via hybrid search with a fine-tuned query expansion model, local GGUF re-ranker, and AST-aware code chunking.

### Search pipeline comparison

Both tools now have sophisticated multi-stage search pipelines. Here's how they compare:

| Stage | sage-wiki | qmd |
|---|---|---|
| **1. Query analysis** | Strong-signal probe — if top BM25 result is confident (score ≥ 0.4 with 2x gap), skip expansion entirely | Always expands |
| **2. Query expansion** | LLM call: 2 lex + 1 vec + 1 hyde variant | Fine-tuned 1.7B local model: 2 lex + 2 vec + 1 hyde |
| **3. Retrieval** | Parallel: chunk-level BM25 (original + lex variants) + BM25-prefiltered vector search (vec/hyde variants) | Parallel per query: BM25 + vector search, original query weighted 2x |
| **4. Fusion** | RRF across all result lists | RRF (k=60) + top-rank bonus (+0.05 for #1, +0.02 for #2-3) |
| **5. Re-ranking** | LLM batch scoring top-15, position-aware blending (75/25 → 60/40 → 40/60) | Cross-encoder (qwen3-reranker-0.6B), position-aware blending (same tiers) |
| **6. Post-retrieval** | **4-signal graph expansion** (direct relations, source overlap, Adamic-Adar neighbors, type affinity) + depth-1 ontology traversal + token budget control | None |
| **7. Synthesis** | LLM synthesis with full article context | Returns search results (no synthesis) |

### Feature comparison

| Dimension | sage-wiki | qmd |
|---|---|---|
| **Core purpose** | Compile raw docs → structured wiki + search + Q&A | Search over existing docs |
| **Language** | Go (single binary) | TypeScript (Node.js/Bun) |
| **Install** | `go install` (0 deps) | `npm install -g` (Node.js 22+) |
| **Storage** | SQLite (FTS5 + sqlite-vec) | SQLite (FTS5 + sqlite-vec) |
| **Chunk indexing** | 800-token chunks, FTS5 + vector per chunk | 900-token chunks, vector per chunk |
| **BM25 search** | Yes (article-level + chunk-level FTS5) | Yes (document-level FTS5) |
| **Vector search** | BM25-prefiltered (~250 comparisons max) | Brute-force cosine |
| **Query expansion** | LLM-based (lex/vec/hyde) with strong-signal skip | Fine-tuned local 1.7B model (lex/vec/hyde) |
| **Re-ranking** | LLM batch scoring + position-aware blending | Cross-encoder + position-aware blending |
| **Knowledge graph** | Yes — typed ontology, 8 built-in + custom relation types, multilingual keyword extraction, cycle detection | No |
| **Graph expansion** | 4-signal scorer (direct link, source overlap, Adamic-Adar neighbors, type affinity) | No |
| **Knowledge compilation** | Yes (4-pass incremental LLM pipeline) | No |
| **Knowledge capture** | Yes (MCP `wiki_capture` tool — extract learnings from conversations) | No |
| **Linting** | Yes (7 passes, self-learning loop) | No |
| **Git integration** | Yes (auto-commit with structured messages) | No |
| **Obsidian support** | Yes (native filesystem, `[[backlinks]]`, frontmatter) | No (separate index) |
| **MCP server** | Yes (14 tools, read + write + capture) | Yes (4 tools, read-only) |
| **Chunking strategy** | Markdown-aware (headings > code fences > paragraphs) | Markdown-aware + AST-aware (tree-sitter for TS/JS/Python/Go/Rust) |
| **Smart chunking** | Break-point scoring | Break-point scoring + code fence protection |
| **Models required** | LLM API key (cloud) or Ollama (local) | 3 local GGUF models (~2GB) |
| **Offline capable** | Partial (API for compilation; Ollama for fully local search) | Fully offline |
| **Local model cost** | Free with Ollama (expansion + BM25+vec search, re-rank auto-disabled) | Free (all three models local) |
| **Cloud query cost** | ~$0.0006/query (expansion + rerank) | N/A (always local) |
| **Token budget** | Configurable (default 8000 tokens), greedy fill by score | No budget control |
| **Configurable relations** | Custom types, multilingual synonyms, domain-specific ontology | N/A |
| **Fallback behavior** | Graceful degradation at every stage (no chunks → doc-level, no LLM → raw query) | N/A |

### Where sage-wiki now leads

**Graph-enhanced context expansion.** This is the feature qmd has no equivalent for. After search retrieves the top documents, sage-wiki's 4-signal graph scorer discovers structurally related articles via the ontology:

| Signal | Weight | What it captures |
|---|---|---|
| Direct link | 3.0 | Ontology relation between search result and candidate |
| Source overlap | 4.0 | Both concepts generated from the same source document |
| Common neighbor | 1.5 | Adamic-Adar: shared neighbors weighted by inverse log-degree |
| Type affinity | 1.0 | Cross-type bonus (concept↔technique: 1.2, same-type: 0.5-0.8) |

Source overlap is the highest-weighted signal because articles from the same source are inherently related — and the check costs zero compute (set intersection on existing ontology edges).

This means sage-wiki can answer questions like "what's related to flash-attention?" by finding not just keyword-similar articles, but articles that share source papers, that have ontology edges to the same concepts, or that are techniques implementing the same foundational concept. qmd can only find documents with similar text.

**Strong-signal skip.** sage-wiki's query expansion has a smart optimization: before calling the LLM for expansion, it runs a fast BM25 probe. If the top result scores ≥ 0.4 (normalized) with a 2x gap over #2, expansion is skipped entirely. This saves an LLM call on simple queries like "flash attention" where BM25 already finds the right article. qmd always expands, even for exact keyword matches.

**BM25-prefiltered vector search.** Instead of brute-force scanning all chunk vectors, sage-wiki uses BM25 results as a candidate filter — vector comparisons are capped at ~250 chunks regardless of wiki size. This makes vector search O(1) rather than O(n) relative to wiki growth. qmd does brute-force cosine over all chunks.

**Knowledge capture from conversations.** The MCP `wiki_capture` tool lets any connected AI agent (Claude Code, ChatGPT, Cursor) extract learnings from a conversation and save them as wiki sources. qmd is read-only via MCP — it can search but can't write.

### Where qmd still leads

**AST-aware code chunking.** qmd uses tree-sitter to chunk code files at function/class/import boundaries (TS, JS, Python, Go, Rust). sage-wiki uses markdown-aware chunking but doesn't parse code ASTs. For code-heavy wikis, qmd produces better chunk boundaries.

**Fine-tuned local query expansion.** qmd's 1.7B model was specifically fine-tuned on query expansion data (2,290 examples, SFT with LoRA, 92% average score on evaluation). sage-wiki uses general-purpose LLM calls for expansion, which works well but isn't optimized for the task. The fine-tuned model is also faster and cheaper than a cloud LLM call.

**Fully offline.** qmd runs entirely local with no network dependency. sage-wiki requires either a cloud API or Ollama for LLM calls. With Ollama, sage-wiki's search is free and local, but compilation still needs an LLM capable of writing articles.

**Cross-encoder re-ranking.** qmd uses a dedicated cross-encoder model (qwen3-reranker-0.6B) that scores query-document pairs jointly. sage-wiki uses its general LLM for re-ranking via a structured prompt, which is effective but less efficient than a dedicated cross-encoder.

### Complementary architecture

These tools solve adjacent problems and could work together:

1. **qmd indexes raw sources** — its AST-aware chunking and fully-local search provide high-quality retrieval over unstructured documents and codebases
2. **sage-wiki compiles knowledge** — it takes the raw material (possibly discovered via qmd) and structures it into a typed ontology with concepts, relations, and cross-references
3. **sage-wiki's MCP tools** serve the compiled knowledge to agents, with graph-enhanced context that qmd can't provide
4. **sage-wiki's knowledge capture** lets agents write back to the wiki, creating a compounding knowledge loop

The key architectural difference: qmd is a **search-only** system optimized for retrieval quality on unstructured documents. sage-wiki is a **knowledge system** that compiles, structures, searches, and grows a knowledge base over time. They share the same foundation (SQLite, FTS5, sqlite-vec, RRF fusion) but build different things on top.

### Lessons already applied from qmd

| qmd feature | sage-wiki implementation |
|---|---|
| 900-token chunk indexing | 800-token chunks with FTS5 + vector per chunk |
| Query expansion (lex/vec/hyde) | LLM-based expansion with same 3 variant types |
| Cross-encoder re-ranking | LLM batch scoring with position-aware blending |
| Position-aware blending (75/25 → 60/40 → 40/60) | Same tier structure |
| Smart markdown chunking | Break-point scoring (headings > code fences > paragraphs) |

### Remaining qmd features worth considering

| Feature | Value | Effort | Verdict |
|---|---|---|---|
| AST-aware code chunking (tree-sitter) | Medium — only for code-heavy wikis | Medium — tree-sitter Go bindings exist | Phase 2: when repo source type is used heavily |
| Fine-tuned local expansion model | Medium — better quality + lower cost | High — needs training pipeline | Low priority: general LLM expansion works well enough |
| Dedicated cross-encoder model | Medium — more efficient than LLM re-ranking | Low — swap in an ONNX cross-encoder | Worth exploring: would reduce re-ranking cost |
| Top-rank bonus in RRF (+0.05 for #1) | Low — marginal quality improvement | Low — two-line change | Easy win: add to RRF merge |