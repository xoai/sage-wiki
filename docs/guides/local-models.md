# Local Model Configuration Guide

sage-wiki supports per-pass model routing — you can use a fast local model
for summarization and extraction while keeping a stronger cloud model for
article writing.

## Quick Start

```yaml
models:
  summarize: ollama/qwen2.5:7b    # local, fast, free
  extract: ollama/qwen2.5:7b      # local, fast, free
  write: claude-sonnet-4-20250514        # cloud, quality matters
```

## Per-Pass Model Recommendations

| Pass | Task | Quality Needed | Recommended |
|------|------|---------------|-------------|
| Summarize | Extract key points from source | Medium | Local 7B+ or cloud mini |
| Extract | Identify concepts from summaries | Medium | Local 7B+ or cloud mini |
| Write | Write wiki articles | High | Cloud (Sonnet/Opus) |
| Lint | Review article quality | Medium | Cloud mini or local 14B |
| Query | Answer questions | High | Cloud (Sonnet/Opus) |

## Setup Options

### GPU (fastest)

Requires Ollama with a CUDA/Metal-capable GPU.

```yaml
api:
  provider: ollama
  base_url: http://localhost:11434

models:
  summarize: qwen2.5:7b      # ~3-5s per source
  extract: qwen2.5:7b        # ~2-3s per batch
  write: qwen2.5:14b         # ~10-15s per article (quality trade-off)
```

**Performance:** 100 sources at 7B with GPU: ~15 minutes (vs ~50 minutes cloud).

### CPU-only

Works on any machine, slower but free.

```yaml
models:
  summarize: ollama/qwen2.5:7b    # ~10-15s per source on CPU
  extract: ollama/qwen2.5:7b
  write: ollama/qwen2.5:14b       # ~30-60s per article
```

**Performance:** 100 sources at 7B on CPU: ~45 minutes.

### Mixed (recommended for large vaults)

Use local models for bulk processing, cloud for quality-sensitive passes.

```yaml
api:
  provider: openai          # or anthropic
  api_key: ${OPENAI_API_KEY}

models:
  summarize: ollama/qwen2.5:7b    # free, fast
  extract: ollama/qwen2.5:7b      # free, fast
  write: gpt-4o                    # cloud, high quality
  query: gpt-4o                    # cloud, high quality
```

**Cost:** ~$0.01 per article (write pass only) vs ~$0.05 per article (all cloud).

## Quality Trade-offs

### Summarize Pass

Local 7B models produce adequate summaries for most content. Quality
differences vs cloud are minimal for:
- Well-structured markdown/prose
- Code files (structure matters more than interpretation)
- Short documents (< 5K tokens)

Quality degrades for:
- Dense academic papers
- Multilingual content
- Documents requiring domain expertise

### Extract Pass

Concept extraction quality is similar between local 7B and cloud for
common topics. Local models may:
- Miss subtle concepts in dense text
- Produce more duplicates (mitigated by embedding-based dedup)
- Struggle with domain-specific terminology

### Write Pass

Article writing shows the largest quality gap between local and cloud.
Local models tend to:
- Produce shorter, less detailed articles
- Include fewer cross-references
- Miss nuanced trade-offs and edge cases

**Recommendation:** Use cloud models for the write pass unless cost is
the primary constraint.

## Ollama Setup

1. Install Ollama: https://ollama.com
2. Pull a model: `ollama pull qwen2.5:7b`
3. Verify: `ollama list`
4. Configure sage-wiki to use `ollama/model-name` format

sage-wiki auto-detects Ollama models when the model name starts with
`ollama/`. The Ollama provider automatically disables reranking for
`sage-wiki query` (local models don't benefit from it). On the search
surfaces — MCP `wiki_search`, `sage-wiki search`, `/api/search`, the TUI —
reranking and query expansion are off by default for every provider and are
requested per call, so there is nothing to disable there.

## Known Limitations

- **Reranking disabled for Ollama:** Local models don't improve with
  reranking. sage-wiki auto-disables it.
- **Embedding quality:** Local embeddings (via Ollama) have lower quality
  than API embeddings. Configure a separate embedding provider if search
  quality matters:
  ```yaml
  embed:
    provider: openai
    model: text-embedding-3-small
  ```
- **Context window:** Some local models have smaller context windows.
  Large sources may need the `split_threshold` config to work well.

## Further Reading

- [Scaling to Large Vaults](large-vault-performance.md) — tiered compilation, backpressure, compile-on-demand
- [Search Quality](search-quality.md) — chunk-level indexing, query expansion, re-ranking
