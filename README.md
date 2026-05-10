**English** | [中文](README_zh.md) | [日本語](README_ja.md) | [한국어](README_ko.md) | [Tiếng Việt](README_vi.md) | [Français](README_fr.md) | [Русский](README_ru.md)

# sage-wiki

An implementation of [Andrej Karpathy's idea](https://x.com/karpathy/status/2039805659525644595) for an LLM-compiled personal knowledge base. Developed using [Sage Framework](https://github.com/xoai/sage).

Some lessons learned after building sage-wiki [here](https://x.com/xoai/status/2040936964799795503).

Drop in your papers, articles, and notes. sage-wiki compiles them into a structured, interlinked wiki — with concepts extracted, cross-references discovered, and everything searchable.

- **Your sources in, a wiki out.** Add documents to a folder. The LLM reads, summarizes, extracts concepts, and writes interconnected articles.
- **Scales to 100K+ documents.** Tiered compilation indexes everything fast, compiles only what matters. A 100K vault is searchable in hours, not months.
- **Compounding knowledge.** Every new source enriches existing articles. The wiki gets smarter as it grows.
- **Works with your tools.** Opens natively in Obsidian. Connects to any LLM agent via MCP. Runs as a single binary — works with API keys or your existing LLM subscription.
- **Ask your wiki questions.** Enhanced search with chunk-level indexing, LLM query expansion, and re-ranking. Ask natural language questions and get cited answers.
- **Compile on demand.** Agents can trigger compilation for specific topics via MCP. Search results signal when uncompiled sources are available.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Dots on the outer boundary represent summaries of all documents in the knowledge base, while dots in the inner circle represent concepts extracted from the knowledge base, with links showing how those concepts connect to one another._

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

| Format      | Extensions                              | What gets extracted                                         |
| ----------- | --------------------------------------- | ----------------------------------------------------------- |
| Markdown    | `.md`                                   | Body text with frontmatter parsed separately                |
| PDF         | `.pdf`                                  | Full text via pure-Go extraction                            |
| Word        | `.docx`                                 | Document text from XML                                      |
| Excel       | `.xlsx`                                 | Cell values and sheet data                                  |
| PowerPoint  | `.pptx`                                 | Slide text content                                          |
| CSV         | `.csv`                                  | Headers + rows (up to 1000 rows)                            |
| EPUB        | `.epub`                                 | Chapter text from XHTML                                     |
| Email       | `.eml`                                  | Headers (from/to/subject/date) + body                       |
| Plain text  | `.txt`, `.log`                          | Raw content                                                 |
| Transcripts | `.vtt`, `.srt`                          | Raw content                                                 |
| Images      | `.png`, `.jpg`, `.gif`, `.webp`, `.svg` | Description via vision LLM (caption, content, visible text) |
| Code        | `.go`, `.py`, `.js`, `.ts`, `.rs`, etc. | Source code                                                 |

Just drop files into your source folder — sage-wiki detects the format automatically. Images require a vision-capable LLM (Gemini, Claude, GPT-4o).

Need a format not listed here? sage-wiki supports **external parsers** — scripts in any language that read stdin and write plain text to stdout. See [External Parsers](#external-parsers) below.

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

| Command                                                                                 | Description                                      |
| --------------------------------------------------------------------------------------- | ------------------------------------------------ |
| `sage-wiki init [--vault] [--skill <agent>]`                                            | Initialize project (greenfield or vault overlay) |
| `sage-wiki compile [--watch] [--dry-run] [--batch] [--estimate] [--no-cache] [--prune]` | Compile sources into wiki articles               |
| `sage-wiki serve [--transport stdio\|sse]`                                              | Start MCP server for LLM agents                  |
| `sage-wiki serve --ui [--port 3333]`                                                    | Start web UI (requires `-tags webui` build)      |
| `sage-wiki lint [--fix] [--pass name]`                                                  | Run linting passes                               |
| `sage-wiki search "query" [--tags ...]`                                                 | Hybrid search (BM25 + vector)                    |
| `sage-wiki query "question"`                                                            | Q&A against the wiki                             |
| `sage-wiki tui`                                                                         | Launch interactive terminal dashboard            |
| `sage-wiki ingest <url\|path>`                                                          | Add a source                                     |
| `sage-wiki status`                                                                      | Wiki stats and health                            |
| `sage-wiki provenance <source-or-concept>`                                              | Show source↔article provenance mappings          |
| `sage-wiki doctor`                                                                      | Validate config and connectivity                 |
| `sage-wiki diff`                                                                        | Show pending source changes against manifest     |
| `sage-wiki list`                                                                        | List wiki entities, concepts, or sources         |
| `sage-wiki write <summary\|article>`                                                    | Write a summary or article                       |
| `sage-wiki ontology <query\|list\|add>`                                                 | Query, list, and manage the ontology graph       |
| `sage-wiki hub <add\|remove\|search\|status\|list>`                                    | Multi-project hub commands                       |
| `sage-wiki learn "text"`                                                                | Store a learning entry                           |
| `sage-wiki capture "text"`                                                              | Capture knowledge from text                      |
| `sage-wiki add-source <path>`                                                           | Register a source file in the manifest           |
| `sage-wiki skill <refresh\|preview> [--target <agent>]`                                 | Generate or refresh agent skill files            |
| `sage-wiki pack install <name\|url>`                                                    | Install a contribution pack                      |
| `sage-wiki pack apply <name> [--mode merge\|replace]`                                   | Apply an installed pack to the project           |
| `sage-wiki pack remove <name>`                                                          | Remove a pack from the project                   |
| `sage-wiki pack list`                                                                   | List applied, cached, and bundled packs          |
| `sage-wiki pack search <query>`                                                         | Search the pack registry                         |
| `sage-wiki pack update [name]`                                                          | Update installed packs to latest versions        |
| `sage-wiki pack info <name>`                                                            | Show details about a pack                        |
| `sage-wiki pack create <name>`                                                          | Scaffold a new pack directory                    |
| `sage-wiki pack validate [path]`                                                        | Validate a pack's schema and files               |
| `sage-wiki pack conflicts`                                                              | Show multi-pack file overlaps                    |
| `sage-wiki auth login --provider <name>`                                                | OAuth login for subscription auth                |
| `sage-wiki auth import --provider <name>`                                               | Import credentials from existing CLI tools       |
| `sage-wiki auth status`                                                                 | Show stored subscription credentials            |
| `sage-wiki auth logout --provider <name>`                                               | Remove stored credentials                        |
| `sage-wiki verify [--all] [--since 7d] [--limit 20]`                                   | Grounding verification on pending outputs        |
| `sage-wiki outputs list [--state pending\|confirmed\|conflict\|stale]`                  | List outputs by trust state                      |
| `sage-wiki outputs promote <id>`                                                        | Manually promote output to confirmed             |
| `sage-wiki outputs reject <id>`                                                         | Reject and delete a pending output               |
| `sage-wiki outputs resolve <id>`                                                        | Promote answer, reject competing conflicts       |
| `sage-wiki outputs clean [--older-than 90d]`                                            | Remove stale/old pending outputs                 |
| `sage-wiki outputs migrate`                                                             | Migrate existing outputs into trust system       |
| `sage-wiki scribe <session-file>`                                                       | Extract entities from a session transcript       |

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
  - path: raw # or vault folders like Clippings/, Papers/
    type: auto # auto-detect from file extension
    watch: true

output: wiki # compiled output directory (_wiki for vault overlay)

# Folders to never read or send to APIs (vault overlay mode)
# ignore:
#   - Daily Notes
#   - Personal

# LLM provider
# Supported: anthropic, openai, gemini, ollama, openai-compatible, qwen
# For OpenRouter or other OpenAI-compatible providers:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# For Alibaba Cloud DashScope Qwen:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY} # env var expansion supported
  # auth: subscription          # use subscription credentials instead of api_key
                                # requires: sage-wiki auth login --provider <name>
                                # supported providers: openai, anthropic, gemini
  # base_url:                   # custom endpoint (OpenRouter, Azure, etc.)
  # rate_limit: 60              # requests per minute
  # extra_params:               # provider-specific params merged into request body
  #   enable_thinking: false    # e.g., disable Qwen thinking mode
  #   reasoning_effort: low     # e.g., DeepSeek reasoning control

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
  provider: auto # auto, openai, gemini, ollama, voyage, mistral
  # model: text-embedding-3-small
  # api_key: ${OPENAI_API_KEY}  # separate key for embeddings
  # base_url:                   # separate endpoint
  # rate_limit: 0              # embedding RPM cap (0 = no limit; set to 1200 for Gemini Tier 1)

# Multi-provider note:
# The api section configures the primary LLM provider used for all compiler
# and query tasks (summarize, extract, write, lint, query). The embed section
# can use a DIFFERENT provider for embeddings — with its own api_key, base_url,
# and rate_limit. This lets you mix providers for cost or quality:
#
#   api:
#     provider: anthropic                    # Claude for generation
#     api_key: ${ANTHROPIC_API_KEY}
#   models:
#     summarize: claude-haiku-4-5-20251001   # cheap model for bulk work
#     write: claude-sonnet-4-20250514        # quality model for articles
#     query: claude-sonnet-4-20250514
#   embed:
#     provider: openai                       # OpenAI for embeddings
#     model: text-embedding-3-small
#     api_key: ${OPENAI_API_KEY}
#
# With subscription auth, you can authenticate with multiple providers:
#   sage-wiki auth login --provider anthropic
#   sage-wiki auth import --provider gemini
# Then use Anthropic for generation and Gemini for embeddings.

compiler:
  max_parallel: 20 # concurrent LLM calls (with adaptive backpressure)
  debounce_seconds: 2 # watch mode debounce
  summary_max_tokens: 2000
  article_max_tokens: 4000
  # extract_batch_size: 20     # summaries per concept-extraction call (reduce to avoid JSON truncation on large corpora)
  # extract_max_tokens: 8192   # max output tokens for concept extraction (increase to 16384 if extraction is truncating)
  auto_commit: true # git commit after compile
  auto_lint: true # run lint after compile
  mode: auto # standard, batch, or auto (auto = batch when 10+ sources)
  # estimate_before: false    # prompt with cost estimate before compiling
  # prompt_cache: true        # enable prompt caching (default: true)
  # batch_threshold: 10       # min sources for auto-batch mode
  # token_price_per_million: 0  # override pricing (0 = use built-in)
  # timezone: Asia/Shanghai   # IANA timezone for user-facing timestamps (default: UTC)
  # article_fields:           # custom frontmatter fields extracted from LLM response
  #   - language
  #   - domain

  # Tiered compilation — index fast, compile what matters
  default_tier: 3 # 0=index, 1=index+embed, 3=full compile
  # tier_defaults:             # per-extension tier overrides
  #   json: 0                  # structured data — index only
  #   yaml: 0
  #   lock: 0
  #   md: 1                    # prose — index + embed
  #   go: 1                    # code — index + embed + parse
  # auto_promote: true         # promote to tier 3 based on query hits
  # auto_demote: true          # demote stale articles
  # split_threshold: 15000     # chars — split large docs for faster writing
  # dedup_threshold: 0.85      # cosine similarity for concept dedup
  # backpressure: true         # adaptive concurrency on rate limits

search:
  hybrid_weight_bm25: 0.7 # BM25 vs vector weight
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
  transport: stdio # stdio or sse
  port: 3333 # SSE mode only

# Output trust — quarantine query outputs until verified
# trust:
#   include_outputs: false       # "false" (default), "verified", "true" (legacy)
#   consensus_threshold: 3       # confirmations for auto-promote
#   grounding_threshold: 0.8     # min grounding score (0.0-1.0)
#   similarity_threshold: 0.85   # question matching threshold
#   auto_promote: true           # auto-promote when all thresholds met

# Ontology types (optional)
# Extend built-in types with additional synonyms or add custom types.
# ontology:
#   relation_types:
#     - name: implements           # extend built-in with more synonyms
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates            # add a custom relation type
#       synonyms: ["regulates", "regulated by", "调控"]
#   entity_types:
#     - name: decision
#       description: "A recorded decision with rationale"
```

### Multi-Provider Setup

sage-wiki lets you use different LLM providers for different tasks. The `api` section sets the primary provider for generation (summarize, extract, write, lint, query), while `embed` can use a completely separate provider for embeddings — each with its own credentials and rate limits.

**Use cases:**
- **Cost optimization** — cheap model for bulk summarization, quality model for article writing
- **Best-of-breed** — Claude for generation, OpenAI for embeddings, Ollama for local search
- **Subscription mixing** — use your ChatGPT subscription for generation and Gemini subscription for embeddings

**Example: Claude for generation + OpenAI embeddings**

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}

models:
  summarize: claude-haiku-4-5-20251001    # cheap for bulk work
  extract: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514         # quality for articles
  lint: claude-haiku-4-5-20251001
  query: claude-sonnet-4-20250514

embed:
  provider: openai
  model: text-embedding-3-small
  api_key: ${OPENAI_API_KEY}
```

**Example: Subscription auth with two providers**

```bash
sage-wiki auth login --provider anthropic
sage-wiki auth import --provider gemini
```

```yaml
api:
  provider: anthropic
  auth: subscription

embed:
  provider: gemini
  # no api_key needed — uses imported Gemini subscription credentials
```

The `models` section controls which model is used per task, all within the primary provider. Different models can have very different cost/quality tradeoffs — use smaller models (haiku, flash, mini) for high-volume passes like summarization, and larger models (sonnet, pro) for article writing and Q&A.

### Configurable Relations

The ontology has 8 built-in relation types: `implements`, `extends`, `optimizes`, `contradicts`, `cites`, `prerequisite_of`, `trades_off`, `derived_from`. Each has default keyword synonyms used for automatic extraction.

You can customize relations via `ontology.relations` in `config.yaml`:

- **Extend a built-in type** — add synonyms (e.g., multilingual keywords) to an existing type. The default synonyms are kept; yours are appended.
- **Add a custom type** — define a new relation name with its keyword synonyms. Relation names must be lowercase `[a-z][a-z0-9_]*`.

Relations are extracted using block-level keyword proximity — a keyword must co-occur with a `[[wikilink]]` in the same paragraph or heading block. This prevents spurious edges from cross-paragraph matches.

You can also restrict which entity types a relation connects:

```yaml
ontology:
  relation_types:
    - name: curated_by
      synonyms: ["curated by", "organized by"]
      valid_sources: [exhibition, program]
      valid_targets: [artist]
```

When `valid_sources`/`valid_targets` are set, edges are only created if the source/target entity type matches. Empty = all types allowed (default).

Zero config = identical to current behavior. Existing databases are migrated automatically on first open. See the [full guide](docs/guides/configurable-relations.md) for domain-specific examples, type-restricted relations, and how extraction works.

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

## Subscription Auth

Use your existing LLM subscription instead of API keys. Supports ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot, and Google Gemini.

```bash
# Login via browser (OpenAI or Anthropic)
sage-wiki auth login --provider openai

# Or import from an existing CLI tool
sage-wiki auth import --provider claude
sage-wiki auth import --provider copilot
sage-wiki auth import --provider gemini
```

Then set `api.auth: subscription` in your `config.yaml`:

```yaml
api:
  provider: openai
  auth: subscription
```

All commands will use your subscription credentials. Tokens refresh automatically. If a token expires and can't refresh, sage-wiki falls back to `api_key` with a warning.

**Limitations:** Batch mode is unavailable with subscription auth (auto-disabled). Some models may not be accessible via subscription tokens. See the [subscription auth guide](docs/guides/subscription-auth.md) for details.

## Output Trust

When sage-wiki answers a question, the answer is an LLM-generated claim, not a verified fact. Without safeguards, wrong answers get indexed into the wiki and pollute future queries. The output trust system quarantines new outputs and requires verification before they enter the searchable corpus.

```yaml
# config.yaml
trust:
  include_outputs: verified  # "false" (exclude all), "verified" (confirmed only), "true" (legacy)
  consensus_threshold: 3     # confirmations needed for auto-promote
  grounding_threshold: 0.8   # minimum grounding score
  similarity_threshold: 0.85 # cosine similarity for question matching
  auto_promote: true          # auto-promote when thresholds met
```

**How it works:**

1. **Query** — sage-wiki answers your question. The output is written to `wiki/under_review/` as pending.
2. **Consensus** — If the same question is asked again and produces the same answer from different source chunks, confirmations accumulate. Independence is scored via Jaccard distance between chunk sets.
3. **Grounding** — Run `sage-wiki verify` to check claims against source passages via LLM entailment.
4. **Promotion** — When both consensus and grounding thresholds are met, the output is promoted to `wiki/outputs/` and indexed into search.

```bash
# Check pending outputs
sage-wiki outputs list

# Run grounding verification
sage-wiki verify --all

# Manually promote a trusted output
sage-wiki outputs promote 2026-05-09-what-is-attention.md

# Resolve a conflict (promote one, reject others)
sage-wiki outputs resolve 2026-05-09-what-is-attention.md

# Clean up old pending outputs
sage-wiki outputs clean --older-than 90d

# Migrate existing outputs into the trust system
sage-wiki outputs migrate
```

Source changes during `sage-wiki compile` automatically demote confirmed outputs when their cited sources are modified. See the [output trust guide](docs/guides/output-trust.md) for the full architecture, configuration reference, and troubleshooting.

## Scaling to Large Vaults

sage-wiki uses **tiered compilation** to handle vaults of 10K-100K+ documents. Instead of compiling everything through the full LLM pipeline, sources are routed through tiers based on file type and usage:

| Tier | What happens | Cost | Time per doc |
|------|-------------|------|-------------|
| **0** — Index only | FTS5 full-text search | Free | ~5ms |
| **1** — Index + embed | FTS5 + vector embedding | ~$0.00002 | ~200ms |
| **2** — Code parse | Structural summary via regex parser (no LLM) | Free | ~10ms |
| **3** — Full compile | Summarize + extract concepts + write articles | ~$0.05-0.15 | ~5-8 min |

By default (`default_tier: 3`), all sources go through the full LLM pipeline — the same behavior as before tiered compilation. For large vaults (10K+), set `default_tier: 1` to index everything in ~5.5 hours, then compile on demand — when an agent queries a topic, search signals uncompiled sources, and `wiki_compile_topic` compiles just that cluster (~2 min for 20 sources).

**Key features:**
- **File-type defaults** — JSON, YAML, and lock files skip to Tier 0 automatically. Configure per-extension via `tier_defaults`.
- **Auto-promotion** — Sources promote to Tier 3 after 3+ search hits or when a topic cluster reaches 5+ sources.
- **Auto-demotion** — Stale articles (90 days without queries) demote to Tier 1 for recompilation on next access.
- **Adaptive backpressure** — Concurrency self-tunes to your provider's rate limits. Starts at 20 parallel, halves on 429s, recovers automatically.
- **10 code parsers** — Go (via go/ast), TypeScript, JavaScript, Python, Rust, Java, C, C++, Ruby, plus JSON/YAML/TOML key extraction. Code gets structural summaries without LLM calls.
- **Compile-on-demand** — `wiki_compile_topic("flash attention")` via MCP compiles relevant sources in real time.
- **Quality scoring** — Per-article source coverage, extraction completeness, and cross-reference density tracked automatically.

See the [full scaling guide](docs/guides/large-vault-performance.md) for configuration, tier override examples, and performance targets.

## Search Quality

sage-wiki uses an enhanced search pipeline for Q&A queries, inspired by analyzing [qmd](https://github.com/dmayboroda/qmd)'s retrieval approach:

- **Chunk-level indexing** — Articles are split into ~800-token chunks, each with its own FTS5 entry and vector embedding. A search for "flash attention" finds the relevant paragraph inside a 3000-token Transformer article.
- **LLM query expansion** — A single LLM call generates keyword rewrites (for BM25), semantic rewrites (for vector search), and a hypothetical answer (for embedding similarity). A strong-signal check skips expansion when the top BM25 result is already confident.
- **LLM re-ranking** — Top 15 candidates are scored by the LLM for relevance. Position-aware blending protects high-confidence retrieval results (ranks 1-3 get 75% retrieval weight, ranks 11+ get 60% reranker weight).
- **Cross-lingual vector search** — Full brute-force cosine search across all chunk vectors, combined with BM25 via RRF fusion. This ensures multilingual queries (e.g., Polish query against English content) find semantically relevant results even when there's zero lexical overlap.
- **Graph-enhanced context expansion** — After retrieval, a 4-signal graph scorer finds related articles via the ontology: direct relations (×3.0), shared source documents (×4.0), common neighbors via Adamic-Adar (×1.5), and entity type affinity (×1.0). This surfaces articles that are structurally related but missed by keyword/vector search.
- **Token budget control** — Query context is capped at a configurable token limit (default 8000), with articles truncated at 4000 tokens each. Greedy filling prioritizes the highest-scored articles.

|                 | sage-wiki                                  | qmd               |
| --------------- | ------------------------------------------ | ----------------- |
| Chunk search    | FTS5 + vector (dual-channel)               | Vector-only       |
| Query expansion | LLM-based (lex/vec/hyde)                   | LLM-based         |
| Re-ranking      | LLM + position-aware blending              | Cross-encoder     |
| Graph context   | 4-signal graph expansion + 1-hop traversal | No graph          |
| Cost per query  | Free (Ollama) / ~$0.0006 (cloud)           | Free (local GGUF) |

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

## Contribution Packs

Packs are installable configuration profiles that bundle ontology types, prompts, and sample sources for specific domains. sage-wiki ships with 8 bundled packs that work offline:

| Pack | Audience | Key ontology |
|------|----------|-------------|
| `academic-research` | Researchers | cites, contradicts, finding, hypothesis |
| `software-engineering` | Dev teams | implements, depends_on, adr, runbook |
| `product-management` | PMs | addresses, prioritizes, user_story |
| `personal-knowledge` | Note-takers | relates_to, inspired_by, fleeting_note |
| `study-group` | Students | explains, prerequisite_of, definition |
| `meeting-organizer` | Managers | decided, assigned_to, action_item |
| `content-creation` | Writers | references, revises, draft, published |
| `legal-compliance` | Legal teams | regulates, supersedes, policy, control |

```bash
# Apply a bundled pack during init
sage-wiki init --pack academic-research

# Or install and apply to an existing project
sage-wiki pack install academic-research
sage-wiki pack apply academic-research --mode merge

# Browse available packs
sage-wiki pack list
sage-wiki pack search "research"

# Install from a Git URL
sage-wiki pack install https://github.com/someone/their-pack.git

# Check for updates
sage-wiki pack update
```

Packs are composable — apply multiple packs and their ontology types are union-merged. Conflicts (overlapping prompt files) are reported. Use `sage-wiki pack conflicts` to inspect.

Community packs are distributed via the [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs) registry. See [CONTRIBUTING.md](CONTRIBUTING.md) for how to create and publish your own pack.

## External Parsers

sage-wiki has built-in parsers for 12+ formats. For anything else — `.docx` templates, `.rtf`, proprietary formats — you can add an external parser as a script in any language.

External parsers use a stdin/stdout protocol: sage-wiki pipes file content to stdin, your script writes plain text to stdout.

```yaml
# parsers/parser.yaml
parsers:
  - extensions: [".rtf"]
    command: python3
    args: ["rtf_parser.py"]
    timeout: 30s
```

```yaml
# config.yaml
parsers:
  external: true          # enable external parser loading
  trust_external: true    # acknowledge that parsers run unsandboxed
```

Security: external parsers run with timeout enforcement (30s default, 120s max) and environment stripping (only PATH, HOME, LANG). They require double opt-in: `parsers.external: true` to load parser definitions, and `parsers.trust_external: true` to acknowledge that parsers execute as unsandboxed subprocesses. Packs with parsers also require `--enable-parsers` during `pack apply`.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full parser authoring guide.

## Agent Skill Files

sage-wiki has 17 MCP tools, but agents won't use them unless something in their context says *when* to check the wiki. Skill files bridge that gap — generated snippets that teach agents when to search, what to capture, and how to query effectively.

```bash
# Generate during project init
sage-wiki init --skill claude-code

# Or add to an existing project
sage-wiki skill refresh --target claude-code

# Preview without writing
sage-wiki skill preview --target cursor
```

This appends a behavioral skill section to the agent's instruction file (CLAUDE.md, .cursorrules, etc.) with project-specific triggers, capture guidelines, and query examples derived from your config.yaml.

**Supported agents:** `claude-code`, `cursor`, `windsurf`, `agents-md` (Antigravity/Codex), `gemini`, `generic`

The skill file provides a generic base — when to search, what to capture, how to query — using your project's entity and relation types from config.yaml. For domain-specific agent behavior (research triggers, meeting capture patterns, etc.), apply a [contribution pack](#contribution-packs):

```bash
sage-wiki init --skill claude-code --pack academic-research
```

The pack's `skills/` directory adds domain-specific triggers alongside the base skill. Running `skill refresh` regenerates only the marked skill section — your other content is preserved.

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

See the full setup guide: [Agent Memory Layer Guide](docs/guides/agent-memory-layer.md)

## Team Setup

sage-wiki scales from a single-person wiki to a shared knowledge base for teams of 3-50. Three deployment patterns:

**Git-synced repo** (3-10 people) — the wiki lives in a Git repository. Everyone clones, compiles locally, and pushes. The compiled `wiki/` directory is tracked; the database is `.gitignore`d and rebuilt on each compile.

**Shared server** (5-30 people) — run sage-wiki on a server with the web UI. Team members browse in the browser and connect agents via MCP over SSE.

**Hub federation** (multi-project) — each project has its own wiki. The hub system federates them into a single search interface with `sage-wiki hub search`.

```bash
# Hub: register and search across multiple wikis
sage-wiki hub add /projects/backend-wiki
sage-wiki hub add /projects/ml-wiki
sage-wiki hub search "deployment process"
```

**What teams get:**

- **Compounding institutional memory.** What one agent learns, all agents know. Decisions, conventions, and gotchas captured from any session are searchable by everyone.
- **Trust-gated outputs.** The [output trust system](docs/guides/output-trust.md) quarantines LLM answers until they're grounding-verified and consensus-confirmed. One agent's hallucination can't poison the shared corpus.
- **Agent skill files.** Generated instructions teach each team member's AI agent when to check the wiki, what to capture, and how to query. Supports Claude Code, Cursor, Windsurf, Codex, and Gemini.
- **Per-user subscription auth.** Each developer uses their own LLM subscription — no shared API keys in the repo. Config says `auth: subscription`; credentials are per-user at `~/.sage-wiki/auth.json`.
- **Full audit trail.** `auto_commit: true` creates a git commit on every compile. Who changed what, when.

```yaml
# Recommended team config
trust:
  include_outputs: verified    # quarantine until verified
compiler:
  default_tier: 1              # index fast, compile on demand
  auto_commit: true            # audit trail
```

See the [full team setup guide](docs/guides/team-setup.md) for source organization, agent integration workflows, knowledge capture pipelines, scaling considerations, and ready-to-use recipes for startups, research labs, and Obsidian vault teams.

## Benchmarks

Evaluated on a real wiki compiled from 1,107 sources (49.4 MB database, 2,832 wiki files).

Run `python3 eval.py .` on your own project to reproduce. See [eval.py](eval.py) for details.

### Performance

| Operation                            |   p50 |   Throughput |
| ------------------------------------ | ----: | -----------: |
| FTS5 keyword search (top-10)         | 411µs |    1,775 qps |
| Vector cosine search (2,858 × 3072d) |  81ms |       15 qps |
| Hybrid RRF (BM25 + vector)           |  80ms |       16 qps |
| Graph traversal (BFS depth ≤ 5)      |   1µs |     738K qps |
| Cycle detection (full graph)         | 1.4ms |            — |
| FTS insert (batch 100)               |     — |    89,802 /s |
| Sustained mixed reads                |  77µs | 8,500+ ops/s |

Non-LLM compile overhead (hashing + dependency analysis) is under 1 second. The compiler's wall time is dominated entirely by LLM API calls.

### Quality

| Metric                    |     Score |
| ------------------------- | --------: |
| Search recall@10          |  **100%** |
| Search recall@1           |     91.6% |
| Source citation rate      |     94.6% |
| Alias coverage            |     90.0% |
| Fact extraction rate      |     68.5% |
| Wiki connectivity         |     60.5% |
| Cross-reference integrity |     50.0% |
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

- **Storage:** SQLite with FTS5 (BM25 search) + BLOB vectors (cosine similarity) + compile_items table for per-source tier/state tracking
- **Ontology:** Typed entity-relation graph with BFS traversal and cycle detection
- **Search:** Enhanced pipeline with chunk-level FTS5 + vector indexing, LLM query expansion, LLM re-ranking, RRF fusion, and 4-signal graph expansion. Search responses signal uncompiled sources for compile-on-demand.
- **Compiler:** Tiered pipeline (Tier 0: index, Tier 1: embed, Tier 2: code parse, Tier 3: full LLM compile) with adaptive backpressure, concurrent Pass 2 extraction, prompt caching, batch API (Anthropic + OpenAI + Gemini), cost tracking, compile-on-demand via MCP, quality scoring, and cascade awareness. Embedding includes retry with exponential backoff, optional rate limiting, and mean-pooling for long inputs. 10 built-in code parsers (Go via go/ast, 8 languages via regex, structured data key extraction).
- **MCP:** 17 tools (6 read, 9 write, 2 compound) via stdio or SSE, including `wiki_compile_topic` for on-demand compilation and `wiki_capture` for knowledge extraction
- **TUI:** bubbletea + glamour 4-tab terminal dashboard (browse, search, Q&A, compile) with tier distribution display
- **Web UI:** Preact + Tailwind CSS embedded via `go:embed` with build tag (`-tags webui`)
- **Scribe:** Extensible interface for ingesting knowledge from conversations. Session scribe processes Claude Code JSONL transcripts.
- **Packs:** Contribution pack system with 8 bundled packs, Git-based registry, install/apply/remove/update lifecycle, transactional apply with snapshot rollback, fill-only merge, and config allowlist security.
- **External Parsers:** Runtime-pluggable file format parsers via stdin/stdout subprocess protocol. Sandboxed execution with timeout, env stripping, and network isolation (Linux).

Zero CGO. Pure Go. Cross-platform.

## License

MIT
