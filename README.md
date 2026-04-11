**English** | [中文](README_zh.md)

# sage-wiki

An implementation of [Andrej Karpathy's idea](https://x.com/karpathy/status/2039805659525644595) for an LLM-compiled personal knowledge base. Developed using [Sage Framework](https://github.com/xoai/sage).

Some lessons learned after building sage-wiki [here](https://x.com/xoai/status/2040936964799795503).

Drop in your papers, articles, and notes. sage-wiki compiles them into a structured, interlinked wiki — with concepts extracted, cross-references discovered, and everything searchable.

- **Your sources in, a wiki out.** Add documents to a folder. The LLM reads, summarizes, extracts concepts, and writes interconnected articles.
- **Compounding knowledge.** Every new source enriches existing articles. The wiki gets smarter as it grows.
- **Works with your tools.** Opens natively in Obsidian. Connects to any LLM agent via MCP. Runs as a single binary — nothing to install beyond the API key.
- **Ask your wiki questions.** Enhanced search with chunk-level indexing, LLM query expansion, and re-ranking. Ask natural language questions and get cited answers.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

*Dots on the outer boundary represent summaries of all documents in the knowledge base, while dots in the inner circle represent concepts extracted from the knowledge base, with links showing how those concepts connect to one another.*

## Install

```bash
# CLI only (no web UI)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# With web UI (requires Node.js for building frontend assets)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Supported Source Formats

| Format | Extensions | What gets extracted |
|--------|-----------|-------------------|
| Markdown | `.md` | Body text with frontmatter parsed separately |
| PDF | `.pdf` | Full text via pure-Go extraction |
| Word | `.docx` | Document text from XML |
| Excel | `.xlsx` | Cell values and sheet data |
| PowerPoint | `.pptx` | Slide text content |
| CSV | `.csv` | Headers + rows (up to 1000 rows) |
| EPUB | `.epub` | Chapter text from XHTML |
| Email | `.eml` | Headers (from/to/subject/date) + body |
| Plain text | `.txt`, `.log` | Raw content |
| Transcripts | `.vtt`, `.srt` | Raw content |
| Images | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | Description via vision LLM (caption, content, visible text) |
| Code | `.go`, `.py`, `.js`, `.ts`, `.rs`, etc. | Source code |

Just drop files into your source folder — sage-wiki detects the format automatically. Images require a vision-capable LLM (Gemini, Claude, GPT-4o).

## Quickstart

![Compiler Pipeline](sage-wiki-compiler-pipeline.png)

### Greenfield (new project)

```bash
mkdir my-wiki && cd my-wiki
sage-wiki init
# Add sources to raw/
cp ~/papers/*.pdf raw/papers/
cp ~/articles/*.md raw/articles/
# Edit config.yaml to add api key, and pick LLMs
# First Compile
sage-wiki compile
# Search
sage-wiki search "attention mechanism"
# Ask questions
sage-wiki query "How does flash attention optimize memory?"
# Interactive terminal dashboard
sage-wiki tui
# Browse in the browser (requires -tags webui build)
sage-wiki serve --ui
# Watch folder
sage-wiki compile --watch
```

### Vault Overlay (existing Obsidian vault)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Edit config.yaml to set source/ignore folders, add api key, pick LLMs
# First Compile
sage-wiki compile
# Watch the vault
sage-wiki compile --watch
```

### Docker

```bash
# Pull from GitHub Container Registry
docker pull ghcr.io/xoai/sage-wiki:latest

# Or from Docker Hub
docker pull xoai/sage-wiki:latest

# Run with your wiki directory mounted
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# Or build from source
docker build -t sage-wiki .
docker run -d -p 3333:3333 -v ./my-wiki:/wiki -e GEMINI_API_KEY=... sage-wiki
```

Available tags: `:latest` (main branch), `:v1.0.0` (releases), `:sha-abc1234` (specific commits). Multi-arch: `linux/amd64` and `linux/arm64`.

See the [self-hosting guide](docs/guides/self-hosted-server.md) for Docker Compose, Syncthing sync, reverse proxy, and LLM provider setup.

## Commands

| Command | Description |
|---------|------------|
| `sage-wiki init [--vault]` | Initialize project (greenfield or vault overlay) |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | Compile sources into wiki articles |
| `sage-wiki serve [--transport stdio\|sse]` | Start MCP server for LLM agents |
| `sage-wiki serve --ui [--port 3333]` | Start web UI (requires `-tags webui` build) |
| `sage-wiki lint [--fix] [--pass name]` | Run linting passes |
| `sage-wiki search "query" [--tags ...]` | Hybrid search (BM25 + vector) |
| `sage-wiki query "question"` | Q&A against the wiki |
| `sage-wiki tui` | Launch interactive terminal dashboard |
| `sage-wiki ingest <url\|path>` | Add a source |
| `sage-wiki status` | Wiki stats and health |
| `sage-wiki provenance <source-or-concept>` | Show source↔article provenance mappings |
| `sage-wiki doctor` | Validate config and connectivity |

## TUI

```bash
sage-wiki tui
```

A full-featured terminal dashboard with 4 tabs:

- **[F1] Browse** — Navigate articles by section (concepts, summaries, outputs). Arrow keys to select, Enter to read with glamour-rendered markdown, Esc to go back.
- **[F2] Search** — Fuzzy search with split-pane preview. Type to filter, results ranked by hybrid score, Enter to open in `$EDITOR`.
- **[F3] Q&A** — Conversational streaming Q&A. Ask questions, get LLM-synthesized answers with source citations. Ctrl+S saves answer to outputs/.
- **[F4] Compile** — Live compile dashboard. Watches source directories for changes and auto-recompiles. Browse compiled files with preview.

Tab switching: `F1`-`F4` from any tab, `1`-`4` on Browse/Compile, `Esc` returns to Browse. Quit with `Ctrl+C`.

## Web UI

![Sage-Wiki Architecture](sage-wiki-webui.png)

sage-wiki includes an optional browser-based viewer for reading and exploring your wiki.

```bash
sage-wiki serve --ui
# Opens at http://127.0.0.1:3333
```

Features:
- **Article browser** with rendered markdown, syntax highlighting, and clickable `[[wikilinks]]`
- **Hybrid search** with ranked results and snippets
- **Knowledge graph** — interactive force-directed visualization of concepts and their connections
- **Streaming Q&A** — ask questions and get LLM-synthesized answers with source citations
- **Table of contents** with scroll-spy, or toggle to graph view
- **Dark/light mode** toggle with system preference detection
- **Broken link detection** — missing article links shown in gray

The web UI is built with Preact + Tailwind CSS and embedded into the Go binary via `go:embed`. It adds ~1.2 MB (gzipped) to the binary size. To build without the web UI, omit the `-tags webui` flag — the binary will still work for all CLI and MCP operations.

Options:
- `--port 3333` — change the port (default 3333)
- `--bind 0.0.0.0` — expose on the network (default localhost only, no auth)

## Configuration

`config.yaml` is created by `sage-wiki init`. Full example:

```yaml
version: 1
project: my-research
description: "Personal research wiki"

# Source folders to watch and compile
sources:
  - path: raw               # or vault folders like Clippings/, Papers/
    type: auto               # auto-detect from file extension
    watch: true

output: wiki                 # compiled output directory (_wiki for vault overlay)

# Folders to never read or send to APIs (vault overlay mode)
# ignore:
#   - Daily Notes
#   - Personal

# LLM provider
# Supported: anthropic, openai, gemini, ollama, openai-compatible
# For OpenRouter or other OpenAI-compatible providers:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}    # env var expansion supported
  # base_url:                   # custom endpoint (OpenRouter, Azure, etc.)
  # rate_limit: 60              # requests per minute

# Model per task — use cheaper models for high-volume, quality for writing
models:
  summarize: gemini-3-flash-preview
  extract: gemini-3-flash-preview
  write: gemini-3-flash-preview
  lint: gemini-3-flash-preview
  query: gemini-3-flash-preview

# Embedding provider (optional — auto-detected from api provider)
# Override to use a different provider for embeddings
embed:
  provider: auto              # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # separate key for embeddings
  # base_url:                   # separate endpoint

compiler:
  max_parallel: 4             # concurrent LLM calls
  debounce_seconds: 2         # watch mode debounce
  summary_max_tokens: 2000
  article_max_tokens: 4000
  auto_commit: true           # git commit after compile
  auto_lint: true             # run lint after compile
  # mode: standard            # standard, batch, or auto
  # estimate_before: false    # prompt with cost estimate before compiling
  # prompt_cache: true        # enable prompt caching (default: true)
  # batch_threshold: 10       # min sources for auto-batch mode
  # token_price_per_million: 0  # override pricing (0 = use built-in)
  # timezone: Asia/Shanghai   # IANA timezone for user-facing timestamps (default: UTC)
  # article_fields:           # custom frontmatter fields extracted from LLM response
  #   - language
  #   - domain

search:
  hybrid_weight_bm25: 0.7    # BM25 vs vector weight
  hybrid_weight_vector: 0.3
  default_limit: 10
  # query_expansion: true     # LLM query expansion for Q&A (default: true)
  # rerank: true              # LLM re-ranking for Q&A (default: true)
  # chunk_size: 800           # tokens per chunk for indexing (100-5000)
  # graph_expansion: true     # graph-based context expansion for Q&A (default: true)
  # graph_max_expand: 10      # max articles added via graph expansion
  # graph_depth: 2            # ontology traversal depth (1-5)
  # context_max_tokens: 8000  # token budget for query context
  # weight_direct_link: 3.0   # graph signal: ontology relation between concepts
  # weight_source_overlap: 4.0 # graph signal: shared source documents
  # weight_common_neighbor: 1.5 # graph signal: Adamic-Adar common neighbors
  # weight_type_affinity: 1.0  # graph signal: entity type pair bonus

serve:
  transport: stdio            # stdio or sse
  port: 3333                  # SSE mode only

# Ontology relation types (optional)
# Extend built-in types with additional synonyms or add custom relation types.
# ontology:
#   relations:
#     - name: implements           # extend built-in with more synonyms
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # add a custom relation type
#       synonyms: ["regulates", "regulated by", "调控"]
```

### Configurable Relations

The ontology has 8 built-in relation types: `implements`, `extends`, `optimizes`, `contradicts`, `cites`, `prerequisite_of`, `trades_off`, `derived_from`. Each has default keyword synonyms used for automatic extraction.

You can customize relations via `ontology.relations` in `config.yaml`:

- **Extend a built-in type** — add synonyms (e.g., multilingual keywords) to an existing type. The default synonyms are kept; yours are appended.
- **Add a custom type** — define a new relation name with its keyword synonyms. Relation names must be lowercase `[a-z][a-z0-9_]*`.

Zero config = identical to current behavior. Existing databases are migrated automatically on first open. See the [full guide](docs/guides/configurable-relations.md) for domain-specific examples, built-in synonym tables, and how extraction works.

## Cost Optimization

sage-wiki tracks token usage and estimates cost for every compile. Three strategies to reduce cost:

**Prompt caching** (default: on) — Reuses system prompts across LLM calls within a compile pass. Anthropic and Gemini cache explicitly; OpenAI caches automatically. Saves 50-90% on input tokens.

**Batch API** — Submit all sources as a single async batch for 50% cost reduction. Available for Anthropic and OpenAI.

```bash
sage-wiki compile --batch       # submit batch, checkpoint, exit
sage-wiki compile               # poll status, retrieve when done
```

**Cost estimation** — Preview cost before committing:

```bash
sage-wiki compile --estimate    # show cost breakdown, exit
```

Or set `compiler.estimate_before: true` in config to prompt every time.

**Auto mode** — Set `compiler.mode: auto` and `compiler.batch_threshold: 10` to automatically use batch when compiling 10+ sources.

## Search Quality

sage-wiki uses an enhanced search pipeline for Q&A queries, inspired by analyzing [qmd](https://github.com/dmayboroda/qmd)'s retrieval approach:

- **Chunk-level indexing** — Articles are split into ~800-token chunks, each with its own FTS5 entry and vector embedding. A search for "flash attention" finds the relevant paragraph inside a 3000-token Transformer article.
- **LLM query expansion** — A single LLM call generates keyword rewrites (for BM25), semantic rewrites (for vector search), and a hypothetical answer (for embedding similarity). A strong-signal check skips expansion when the top BM25 result is already confident.
- **LLM re-ranking** — Top 15 candidates are scored by the LLM for relevance. Position-aware blending protects high-confidence retrieval results (ranks 1-3 get 75% retrieval weight, ranks 11+ get 60% reranker weight).
- **BM25-prefiltered vector search** — Vector comparisons are limited to chunks from BM25 candidate documents, capping cosine computations at ~250 regardless of wiki size.
- **Graph-enhanced context expansion** — After retrieval, a 4-signal graph scorer finds related articles via the ontology: direct relations (×3.0), shared source documents (×4.0), common neighbors via Adamic-Adar (×1.5), and entity type affinity (×1.0). This surfaces articles that are structurally related but missed by keyword/vector search.
- **Token budget control** — Query context is capped at a configurable token limit (default 8000), with articles truncated at 4000 tokens each. Greedy filling prioritizes the highest-scored articles.

| | sage-wiki | qmd |
|---|---|---|
| Chunk search | FTS5 + vector (dual-channel) | Vector-only |
| Query expansion | LLM-based (lex/vec/hyde) | LLM-based |
| Re-ranking | LLM + position-aware blending | Cross-encoder |
| Graph context | 4-signal graph expansion + 1-hop traversal | No graph |
| Cost per query | Free (Ollama) / ~$0.0006 (cloud) | Free (local GGUF) |

Zero config = all features enabled. With Ollama or other local models, enhanced search is completely free — re-ranking is auto-disabled (local models struggle with structured JSON scoring) but chunk-level search and query expansion still work. With cloud LLMs, the additional cost is negligible (~$0.0006/query). Both expansion and re-ranking can be toggled via config. See the [full search quality guide](docs/guides/search-quality.md) for configuration, cost breakdown, and detailed comparison.

## Customizing Prompts

sage-wiki uses built-in prompts for summarization and article writing. To customize:

```bash
sage-wiki init --prompts    # scaffolds prompts/ directory with defaults
```

This creates editable markdown files:

```
prompts/
├── summarize-article.md    # how articles are summarized
├── summarize-paper.md      # how papers are summarized
├── write-article.md        # how concept articles are written
├── extract-concepts.md     # how concepts are identified
└── caption-image.md        # how images are described
```

Edit any file to change how sage-wiki processes that type. Add new source types by creating `summarize-{type}.md` (e.g., `summarize-dataset.md`). Delete a file to revert to the built-in default.

### Custom Frontmatter Fields

Article frontmatter is built from two sources: **ground-truth data** (concept name, aliases, sources, timestamp) is always generated by code, while **semantic fields** are assessed by the LLM.

By default, `confidence` is the only LLM-assessed field. To add custom fields:

1. Declare them in `config.yaml`:

```yaml
compiler:
  article_fields:
    - language
    - domain
```

2. Update your `prompts/write-article.md` template to ask the LLM for these fields:

```
At the end of your response, state:
Language: (the primary language of the concept)
Domain: (the academic field, e.g., machine learning, biology)
Confidence: high, medium, or low
```

The LLM's responses are extracted from the article body and merged into the YAML frontmatter automatically. The resulting frontmatter looks like:

```yaml
---
concept: self-attention
aliases: ["scaled dot-product attention"]
sources: ["raw/transformer-paper.md"]
confidence: high
language: English
domain: machine learning
created_at: 2026-04-10T08:00:00+08:00
---
```

Ground-truth fields (`concept`, `aliases`, `sources`, `created_at`) are always accurate — they come from the extraction pass, not the LLM. Semantic fields (`confidence` + your custom fields) reflect the LLM's judgment.

## MCP Integration

![MCP Integration](sage-wiki-interfaces.png)

### Claude Code

Add to `.mcp.json`:

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/wiki"]
    }
  }
}
```

### SSE (network clients)

```bash
sage-wiki serve --transport sse --port 3333
```

## Knowledge Capture from AI Conversations

sage-wiki runs as an MCP server, so you can capture knowledge directly from your AI conversations. Connect it to Claude Code, ChatGPT, Cursor, or any MCP client — then just ask:

> "Save what we just figured out about connection pooling to my wiki"

> "Capture the key decisions from this debugging session"

The `wiki_capture` tool extracts knowledge items (decisions, discoveries, corrections) from conversation text via your LLM, writes them as source files, and queues them for compilation. Noise (greetings, retries, dead ends) is filtered out automatically.

For single facts, `wiki_learn` stores a nugget directly. For full documents, `wiki_add_source` ingests a file. Run `wiki_compile` to process everything into articles.

See the full setup guide: [MCP Knowledge Capture Guide](docs/guides/mcp-knowledge-capture.md)

## Benchmarks

Evaluated on a real wiki compiled from 1,107 sources (49.4 MB database, 2,832 wiki files).

Run `python3 eval.py .` on your own project to reproduce. See [eval.py](eval.py) for details.

### Performance

| Operation | p50 | Throughput |
|---|--:|--:|
| FTS5 keyword search (top-10) | 411µs | 1,775 qps |
| Vector cosine search (2,858 × 3072d) | 81ms | 15 qps |
| Hybrid RRF (BM25 + vector) | 80ms | 16 qps |
| Graph traversal (BFS depth ≤ 5) | 1µs | 738K qps |
| Cycle detection (full graph) | 1.4ms | — |
| FTS insert (batch 100) | — | 89,802 /s |
| Sustained mixed reads | 77µs | 8,500+ ops/s |

Non-LLM compile overhead (hashing + dependency analysis) is under 1 second. The compiler's wall time is dominated entirely by LLM API calls.

### Quality

| Metric | Score |
|---|--:|
| Search recall@10 | **100%** |
| Search recall@1 | 91.6% |
| Source citation rate | 94.6% |
| Alias coverage | 90.0% |
| Fact extraction rate | 68.5% |
| Wiki connectivity | 60.5% |
| Cross-reference integrity | 50.0% |
| **Overall quality score** | **73.0%** |

### Running the eval

```bash
# Full evaluation (performance + quality)
python3 eval.py /path/to/your/wiki

# Performance only
python3 eval.py --perf-only .

# Quality only
python3 eval.py --quality-only .

# Machine-readable JSON
python3 eval.py --json . > report.json
```

Requires Python 3.10+. Install `numpy` for ~10x faster vector benchmarks.

### Running the tests

```bash
# Run the full test suite (generates synthetic fixtures, no real data needed)
python3 -m unittest eval_test -v

# Generate a standalone test fixture
python3 eval_test.py --generate-fixture ./test-fixture
python3 eval.py ./test-fixture
```

24 tests covering: fixture generation, CLI modes (`--perf-only`, `--quality-only`, `--json`), JSON schema validation, score bounds, search recall, edge cases (empty wikis, large datasets, missing paths).

## Architecture

![Sage-Wiki Architecture](sage-wiki-architecture.png)

- **Storage:** SQLite with FTS5 (BM25 search) + BLOB vectors (cosine similarity)
- **Ontology:** Typed entity-relation graph with BFS traversal and cycle detection
- **Search:** Enhanced pipeline with chunk-level FTS5 + vector indexing, LLM query expansion, LLM re-ranking, RRF fusion, and 4-signal graph expansion. Falls back to document-level BM25 + vector + tag boost + recency decay
- **Compiler:** 5-pass pipeline (diff, summarize, extract concepts, write articles, images) with prompt caching, batch API, cost tracking, and cascade awareness for source removal
- **MCP:** 16 tools (6 read, 8 write, 2 compound) via stdio or SSE, including `wiki_capture` for knowledge extraction and `wiki_provenance` for source↔article mappings
- **TUI:** bubbletea + glamour 4-tab terminal dashboard (browse, search, Q&A, compile)
- **Web UI:** Preact + Tailwind CSS embedded via `go:embed` with build tag (`-tags webui`)

Zero CGO. Pure Go. Cross-platform.

## License

MIT
