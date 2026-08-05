**English** | [中文](docs/translations/README_zh.md) | [日本語](docs/translations/README_ja.md) | [한국어](docs/translations/README_ko.md) | [Tiếng Việt](docs/translations/README_vi.md) | [Français](docs/translations/README_fr.md) | [Русский](docs/translations/README_ru.md)

# sage-wiki

**sage-wiki** is a graph memory and knowledge base that AI agents and humans build and query together. Drop in documents; an LLM compiler turns them into an interlinked wiki with a knowledge graph — agents query it through MCP, humans browse it as plain markdown. Enable the opt-in graph passes and it becomes an *evidenced* graph: typed entities, provenance-bearing relations, resolved aliases, and per-fact citations on answers. One Go binary scales it from a personal vault to a team hub to a company knowledge graph.

**→ Get started: [Install](#install) · [Quickstart](#quickstart)**

Grown from [Andrej Karpathy's idea](https://x.com/karpathy/status/2039805659525644595) of an LLM-compiled personal knowledge base, built with the [Sage Framework](https://github.com/xoai/sage). Some lessons learned along the way [here](https://x.com/xoai/status/2040936964799795503).

- **Graph memory with citations.** Ask relational questions through `wiki_graph_query` — answers are grounded only in serialized graph edges; with the evidenced graph enabled, each citation carries its source document and confidence.
- **Built for agents and humans.** 19 MCP tools plus generated skill files teach agents when to search, capture, and compile; humans get Obsidian-native markdown, a TUI, and a web UI over the same data.
- **Trust and provenance.** Query outputs quarantine until verified; every evidenced relation records which document asserted it.
- **Your sources in, a wiki out.** The compile pipeline reads papers, notes, code, and email; summarizes; extracts concepts; and writes interconnected articles — the ingestion layer for everything above. Every new source enriches existing articles; the wiki compounds as it grows.
- **Ask your wiki questions.** Hybrid chunk-level search with LLM query expansion, re-ranking, and graph-aware context assembly returns cited answers.
- **Scales to 100K+ documents.** Tiered compilation indexes everything fast and spends LLM budget only where it matters.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

_Dots on the outer boundary represent summaries of all documents in the knowledge base, while dots in the inner circle represent concepts extracted from the knowledge base, with links showing how those concepts connect to one another._

## From personal vault to company knowledge graph

- **Personal** — overlay an existing Obsidian vault (`init --vault`), run on [local models](docs/guides/local-models.md) for zero cost, and opt into the graph passes (`ontology.triples` + `ontology.resolve`) when you want the evidenced graph.
- **Team** — share one wiki via git or a [self-hosted server](docs/guides/self-hosted-server.md), review entity-resolution proposals and [output trust](docs/guides/output-trust.md) together, and federate multiple wikis with the hub. See [Team Setup](docs/guides/team-setup.md).
- **Company** — move storage to [PostgreSQL/pgvector](docs/guides/storage-backends.md), turn on [metrics](docs/guides/metrics.md), front the server with auth, and scale ingestion with [tiered compilation](docs/guides/large-vault-performance.md).

## Knowledge graph & graph memory

![sage-wiki graph engine](assets/sage-wiki-graph-engine.png)

Vector search retrieves passages that *look like* the query. A graph also
records **how things relate**, so a question needing two or three hops is
answered by traversal instead of hoping one chunk happens to contain the whole
chain. sage-wiki builds that graph as a compile output — not a second database
you have to keep in sync.

- **Entities and typed relations.** Each compile extracts entities (concepts,
  sources, artifacts) and links them with typed relations. The relation
  vocabulary is yours to define — see
  [configurable relations](docs/guides/configurable-relations.md).
- **Evidenced edges.** A relation can carry `evidence` (the span that supports
  it), `confidence` (0–1), and `source_doc`, so a conclusion traces to the
  sentence that justified the edge rather than to a whole document.
- **Triples.** An optional structured-output pass extracts
  subject → relation → object directly. Opt-in (`ontology.triples`): it adds
  one LLM call per document, and defaults never spend your key without asking.
- **Entity resolution.** "K8s" and "Kubernetes" become one node. Proposals are
  review-gated by default rather than silently merged.

**The graph is a retrieval channel, not a side view.** Every search fuses three
channels — lexical (BM25), vector, and graph proximity: query terms seed
entities, a bounded traversal ranks their neighborhood, and the three fuse at
`search.hybrid_weight_graph`. An empty ontology costs nothing and leaves
results byte-identical, so the graph earns its place incrementally.

Query it directly, or let an agent do it over MCP:

```bash
sage-wiki ontology query --entity kubernetes --depth 3 --direction both
sage-wiki provenance "service mesh"    # which sources produced this concept
```

Edges are bi-temporal: contradicting a fact invalidates the old edge instead
of colliding, default answers are contradiction-free, and `as_of` queries
answer "what did we believe in January?" Ambiguous contradictions still
surface through [output trust](docs/guides/output-trust.md) review. For
corpus-wide questions ("main themes across everything?"), opt-in community
detection (`ontology.communities.enabled`) generates cached community
summaries and answers via `wiki_graph_query` `mode: "global"`. Depth
and mechanics: [graph memory](docs/guides/graph-memory.md).

## Guides

| Guide | Description |
|-------|-------------|
| [Agent Memory Layer](docs/guides/agent-memory-layer.md) | MCP setup, skill files, capture workflows, read-capture-evolve loop |
| [HTTP API](docs/guides/http-api.md) | The /v1 REST surface: auth, error model, idempotency, async jobs |
| [Graph Memory](docs/guides/graph-memory.md) | Evidenced relations, triple extraction, entity resolution, graph QA |
| [Configuration](docs/guides/configuration.md) | The full annotated config.yaml, multi-provider setup, serve worker |
| [Team Setup](docs/guides/team-setup.md) | Git-synced, shared server, and hub federation deployment patterns |
| [Search Quality](docs/guides/search-quality.md) | Chunk indexing, query expansion, re-ranking, graph expansion, ANN |
| [Large Vault Performance](docs/guides/large-vault-performance.md) | Tiered compilation, backpressure, code parsers, 100K+ scaling |
| [Output Trust](docs/guides/output-trust.md) | Grounding verification, consensus, promotion/demotion lifecycle |
| [Subscription Auth](docs/guides/subscription-auth.md) | OAuth login, token import, credential management |
| [Self-Hosted Server](docs/guides/self-hosted-server.md) | Docker Compose, Syncthing, reverse proxy, VPS deployment |
| [Storage Backends](docs/guides/storage-backends.md) | SQLite vs PostgreSQL/pgvector setup, switching, pool sizing |
| [Configurable Relations](docs/guides/configurable-relations.md) | Custom ontology types, multilingual synonyms, type restrictions |
| [Customizing Prompts](docs/guides/customizing-prompts.md) | Prompt scaffolding, per-type overrides, custom frontmatter fields |
| [Local Models](docs/guides/local-models.md) | Ollama setup, GPU/CPU routing, per-pass model config |
| [Metrics](docs/guides/metrics.md) | Log snapshots, /metrics endpoint, cardinality controls |
| [Webhooks](docs/webhooks.md) | HMAC-signed event delivery, signature recipe, retry/dead-letter |
| [Security](docs/security.md) | Threat model, the limits table, prompt boundary, residual risks |
| [Contribution Packs](CONTRIBUTING.md) | Creating packs, parser authoring, registry submission |

## Install

```bash
# CLI only (no web UI)
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest

# With web UI (requires Node.js for building frontend assets)
git clone https://github.com/xoai/sage-wiki.git && cd sage-wiki
cd web && npm install && npm run build && cd ..
go build -tags webui -o sage-wiki ./cmd/sage-wiki/
```

## Quickstart

![Compiler Pipeline](assets/sage-wiki-compiler-pipeline.png)

### Greenfield (new project)

```bash
sage-wiki init my-wiki && cd my-wiki
# Add sources to raw/
cp ~/papers/*.pdf raw/
# Edit config.yaml to add api key, and pick LLMs
sage-wiki compile                                  # first compile
sage-wiki compile                                  # second: zero LLM calls — unchanged docs are skipped
sage-wiki compile --explain raw/paper.pdf          # why a doc compiles or skips
sage-wiki compile --force                          # recompile everything regardless
sage-wiki search "attention mechanism"             # hybrid search
sage-wiki query "How does flash attention work?"   # cited Q&A
sage-wiki tui                                      # terminal dashboard
sage-wiki serve --ui                               # browser (webui build)
sage-wiki compile --watch                          # watch folder
```

Every `config.yaml` key, annotated line by line: [Configuration](docs/guides/configuration.md).

**Project layout** (what `init` creates — selected entries, illustrative not exhaustive):

```
my-wiki/
├── config.yaml           # providers, models, compiler, search, ontology
├── raw/                  # drop sources here (articles, papers, code, images)
├── wiki/                 # compiled output — Obsidian-compatible markdown
│   ├── summaries/        # per-source LLM summaries
│   ├── concepts/         # concept articles (the knowledge graph)
│   ├── images/           # vision-captioned image descriptions
│   ├── outputs/          # filed query answers (trust.include_outputs: "true")
│   ├── under_review/     # filed answers awaiting trust review (default)
│   └── archive/          # pruned articles
├── .sage/wiki.db         # one SQLite file: FTS index, vectors, ontology, queue
└── .manifest.json        # source↔article mapping + compile state
```

### Vault Overlay (existing Obsidian vault)

```bash
cd ~/Documents/MyVault
sage-wiki init --vault
# Edit config.yaml to set source/ignore folders, add api key, pick LLMs
sage-wiki compile --watch
```

Prefer containers? Prebuilt multi-arch Docker images and compose files are
covered in the [self-hosted server guide](docs/guides/self-hosted-server.md).

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
| Images      | `.png`, `.jpg`, `.gif`, `.webp`, `.svg`, `.bmp` | Description via vision LLM (caption, content, visible text) |
| Code        | `.go`, `.py`, `.js`, `.ts`, `.rs`, etc. | Source code                                                 |

Just drop files into your source folder — sage-wiki detects the format automatically. Images require a vision-capable LLM (Gemini, Claude, GPT-4o). Need a format not listed? sage-wiki supports [external parsers](#external-parsers) — scripts in any language reading stdin, writing text to stdout.

## Graph memory

Out of the box the wiki builds a knowledge graph from keyword proximity —
concepts linked where relation keywords co-occur with a `[[wikilink]]` in
the same block. Enable the
**opt-in graph passes** to turn that into an evidenced graph:

- **Triple extraction** (`ontology.triples.enabled`) — one extra LLM call
  per fully-compiled document extracts typed entities and relations, each
  carrying an evidence span, confidence, and source document.
- **Entity resolution** (`ontology.resolve.enabled`) — surface-form
  variants ("NASA" / "National Aeronautics and Space Administration")
  are linked to a canonical entity. High-confidence proposals apply
  automatically (threshold 0.85; set exactly `1.0` for review-only), and
  every link is exactly reversible with `ontology resolve --unlink`.
- **Graph QA** — the `wiki_graph_query` MCP tool answers multi-hop
  relational questions grounded *only* in a bounded, serialized set of
  edges; citations carry `source_doc` and `confidence` when the edge is
  evidenced (keyword-proximity edges carry neither). Regular Q&A
  context also names the connecting edge under each related article.

Depth, costs, review workflow, and undo semantics: [Graph Memory](docs/guides/graph-memory.md).

## Commands

The core surface; run `sage-wiki <command> --help` for flags.

| Command | Description |
| ------- | ----------- |
| `sage-wiki init [dir] [--vault] [--skill <agent>] [--pack <name>] [--prompts] [--force]` | Initialize project (greenfield or vault overlay); preserves existing config/manifest/gitignore unless `--force` |
| `sage-wiki compile [--watch] [--batch] [--estimate] [--dry-run] [--no-cache] [--fresh] [--re-embed] [--re-extract] [--prune]` | Compile sources into wiki articles |
| `sage-wiki serve [--addr 127.0.0.1:8484] [--transport stdio\|sse] [--ui]` | HTTP REST + MCP server / web UI |
| `sage-wiki reindex [--drop-chunk-vectors]` | Rebuild the chunk index from documents on disk with the current `chunk_size`/`chunk_overlap_tokens` |
| `sage-wiki index rebuild-vectors [--quantize none\|int8]` | Rebuild the on-disk vector index (`.sage/vectors*.idx`) from the stored embeddings — required once after enabling `vectors.backend: mmap` and again after compiles/re-embeds |
| `sage-wiki search "query" [--tags ...] [--boost-tags ...] [--limit N] [--channels bm25,vector,graph] [--expand] [--rerank]` | Hybrid search (BM25 + vector + ontology graph) |
| `sage-wiki query "question"` | Q&A against the wiki with citations |
| `sage-wiki tui` | Interactive terminal dashboard |
| `sage-wiki ontology <query\|list\|add\|resolve>` | Query, manage, and resolve the ontology graph |
| `sage-wiki ingest <url\|path>` / `sage-wiki add-source <path>` | Add sources |
| `sage-wiki source <show\|list>` / `sage-wiki coverage` | Inspect sources and compile coverage |
| `sage-wiki status` / `sage-wiki doctor` / `sage-wiki diff` | Health, config validation, pending changes |
| `sage-wiki lint [--fix]` / `sage-wiki list` / `sage-wiki write <summary\|article>` | Maintenance and manual writes |
| `sage-wiki hub <init\|add\|remove\|search\|status\|list\|compile>` | Multi-project hub |
| `sage-wiki learn "text"` / `sage-wiki capture "text"` / `sage-wiki scribe <session-file>` | Knowledge capture |
| `sage-wiki skill <refresh\|preview> [--target <agent>]` | Generate or refresh agent skill files |
| `sage-wiki provenance <source-or-concept>` / `sage-wiki version` | Provenance mappings, version |
| `sage-wiki mirror <enable\|status\|snapshot\|verify>` | Remote mirror (S3-compatible backup, WAL shipping) — see [Remote mirror](#remote-mirror-s3-backup) |
| `sage-wiki hydrate s3://<bucket>/<prefix> <DIR>` | Restore a workspace from a remote mirror into an empty dir (`--generation`, `--at`, `--partial`, `--key-file`) |

Topic-specific command families live with their guides: `pack *` in
[CONTRIBUTING](CONTRIBUTING.md), `auth *` (login, import, status, logout,
migrate) in [Subscription Auth](docs/guides/subscription-auth.md), and
`verify` / `outputs *` in [Output Trust](docs/guides/output-trust.md).

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

```bash
sage-wiki serve --ui        # http://127.0.0.1:3333, requires -tags webui build
```

- **Article browser** with rendered markdown, syntax highlighting, and clickable `[[wikilinks]]`
- **Hybrid search** with ranked results and snippets
- **Knowledge graph** — interactive force-directed visualization of concepts and their connections
- **Streaming Q&A** — ask questions and get LLM-synthesized answers with source citations
- **Table of contents** with scroll-spy; dark/light mode with system preference detection; broken article links shown in gray

Built with Preact + Tailwind, embedded via `go:embed` (~1.2 MB, ~420 KB gzipped); omit `-tags webui` for a CLI/MCP-only binary. Auth tokens, allowed hosts, and deployment hardening: [Self-Hosted Server](docs/guides/self-hosted-server.md).

## MCP Integration

![MCP Integration](assets/sage-wiki-interfaces.png)

Add to `.mcp.json` (Claude Code; other agents in the [Agent Memory Layer guide](docs/guides/agent-memory-layer.md)):

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--transport", "stdio", "--project", "/path/to/wiki"]
    }
  }
}
```

Network clients: `sage-wiki serve --transport sse --port 3333`. The server
exposes 19 tools — search, read, graph query, capture, `wiki_query`
(question answering with trust-reviewed filing), compile-on-demand and
more; setup per agent and capture workflows live in the
[Agent Memory Layer guide](docs/guides/agent-memory-layer.md).

**HTTP API (`/v1`, experimental)** — any language can call the same tools
over REST: `sage-wiki serve --ui --port 3333` mounts 20 routes under `/v1`
(Bearer auth via `SAGE_WIKI_TOKEN`, structured errors, idempotent writes).
Long-running compile/lint run as async jobs: `POST /v1/jobs/compile` or
`POST /v1/jobs/lint` returns `202` + a `job_id` to poll.
Contract: [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1, drift-checked
against the MCP tool set). Guide: [docs/guides/http-api.md](docs/guides/http-api.md).
Pre-1.0 — pin a version.

**Agent skill files** — `sage-wiki skill refresh --target <agent>` writes
a behavioral section into the agent's instruction file (CLAUDE.md,
.cursorrules, …) teaching it when to search, what to capture, and how to
query, derived from your config. Targets: `claude-code`, `cursor`,
`windsurf`, `agents-md` (Antigravity), `codex`, `gemini`, `generic`.

### Agent skills

Install sage-wiki's reference skill so a coding assistant knows the full
tool surface — all 19 MCP tools, the `/v1` REST equivalents, opt-in flags,
tiers, async compile semantics, and error codes — without reading this
README:

```bash
# Claude Code
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki

# Or manually: copy skills/sage-wiki/SKILL.md to .claude/skills/
```

The `sage-wiki-integrate` pipeline skill wires sage-wiki into a new repo
interactively (detect language → install client or configure MCP →
smoke-test store-and-retrieve):

```bash
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki-integrate
```

Both skills are generated from the live MCP registry
(`go run ./tools/skillgen/`) and drift-checked in CI — they cannot go stale
when tools change. Pre-1.0 — pin a version.

**Knowledge capture** — agents store insights back via `wiki_capture` /
`wiki_learn`, closing the read-capture-evolve loop. Workflows and tips:
[Agent Memory Layer](docs/guides/agent-memory-layer.md).

## Client SDKs

Typed clients over the `/v1` REST API (pre-1.0 — pin a version):

**Python** — `pip install sagewiki` (≥3.9, `httpx` only):

```python
from sagewiki import SageWiki

c = SageWiki()  # SAGE_WIKI_URL / SAGE_WIKI_TOKEN from env
for r in c.search("attention", limit=5).results:
    print(r.final_score, r.content[:80])
job = c.compile(topic="attention")
job.wait(timeout=600)  # explicit timeout required
```

**TypeScript** — `npm install sagewiki` (zero runtime dependencies, global
`fetch`; Node ≥18, Deno, Bun, edge runtimes):

```ts
import { SageWikiClient } from "sagewiki";

const c = new SageWikiClient();
const results = await c.search("attention", { limit: 5 });
const job = await c.compile({ topic: "attention" });
await job.waitUntilDone({ timeoutMs: 600_000 });
```

Both clients cover the full `/v1` surface: search, provenance, graph
queries, the compiled wiki, captures/writes, and async compile/lint jobs
with a code-driven error taxonomy. Docs:
[Python](clients/python/README.md) · [TypeScript](clients/typescript/README.md) ·
[HTTP API guide](docs/guides/http-api.md). Go programs can skip HTTP
entirely — see [Embedding in a Go program](#embedding-in-a-go-program).

### Examples

Copy-paste framework integrations, exercised in CI against a live server:

- [`examples/langgraph/`](examples/langgraph/) — memory-backed LangGraph
  nodes (Python client): retrieval with the `uncompiled_sources` →
  topic-compile pattern, plus capture.
- [`examples/vercel-ai-sdk/`](examples/vercel-ai-sdk/) — `search`,
  `graphQuery`, `provenance` as Vercel AI SDK tools (TypeScript client);
  edge-deployable.

### Embedding in a Go program

To call the same tools from your own Go process — no subprocess, no stdio or
port to manage — use `pkg/sagewiki` with mcp-go's in-process transport:

```go
srv, err := sagewiki.NewServer("/path/to/wiki")  // project must already exist
if err != nil {
    return err
}
defer srv.Close()  // the caller owns the DB handle here

cli, err := client.NewInProcessClient(srv.MCPServer())
if err != nil {
    return err
}
defer cli.Close()

if err := cli.Start(ctx); err != nil {
    return err
}
if _, err := cli.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
    return err
}

res, err := cli.CallTool(ctx, mcp.CallToolRequest{
    Params: mcp.CallToolParams{
        Name:      "wiki_search",
        Arguments: map[string]any{"query": "attention", "limit": 5},
    },
})
```

The project must already exist and the caller owns the database handle, so
`Close` is required — unlike `serve`, nothing else closes it. Logs go to the
host's stderr, and `initialize` reports the sage-wiki build version (`dev` from
a plain `go build`); call `sagewiki.SetVersion` at startup to report your own
version string instead.

The package is **experimental** while sage-wiki is pre-1.0: the Go signatures
are meant to stay put, but tool names, argument schemas, and `config.yaml`
layout can change in any release. Pin a version.

## Operations

- **Storage** — SQLite by default (single file, zero config); PostgreSQL +
  pgvector for server deployments. Switching and pool sizing: [Storage Backends](docs/guides/storage-backends.md).
- **Observability** — structured log snapshots and an opt-in `/metrics`
  endpoint: [Metrics](docs/guides/metrics.md).
- **Events & webhooks** — the engine emits a typed event stream (captures,
  compile lifecycle, per-doc outcomes, graph edges, searches, LLM usage) to a
  rotating JSONL audit trail (`events:` block); serve mode adds
  HMAC-SHA256-signed webhooks and an SSE stream at `/events/stream`. Events
  never carry document content or raw query text. See
  [Webhooks](docs/webhooks.md) and [Configuration § events](docs/guides/configuration.md#events).
- **Structured outputs** — LLM extraction passes use each provider's
  native mechanism (Anthropic tool-use, OpenAI `response_format`, Gemini
  `responseSchema`) with a validating fence-strip fallback.
- **Credentials** — subscription tokens live in the OS keychain where
  available; run `sage-wiki auth migrate` once to move file-stored
  credentials over. [Subscription Auth](docs/guides/subscription-auth.md).
- **Configuration** — every key, annotated, with multi-provider recipes
  and the serve-mode compile worker: [Configuration](docs/guides/configuration.md).
- **Entity resolution** — 0.85 auto-apply, exactly reversible with `--unlink`; see [Graph memory](#graph-memory) above.
- **Custom relation/entity types** — extend built-ins or add your own
  (`ontology.relation_types`), with multilingual synonyms and type
  restrictions: [Configurable Relations](docs/guides/configurable-relations.md).
- **Output trust** — query outputs quarantine until grounded, confirmed by
  consensus, or manually promoted: [Output Trust](docs/guides/output-trust.md).
- **Search tuning** — chunking, expansion, re-ranking, graph expansion,
  and opt-in ANN: [Search Quality](docs/guides/search-quality.md).

### Embedding (Go API)

sage-wiki can be embedded as a Go module — the same engine the CLI runs
on, workspace-scoped and offline-capable:

```go
w, err := engine.Open(ctx, dir)          // one Workspace per directory
defer w.Close()
id, _ := w.Capture(ctx, engine.Source{Path: "doc.md"})
res, _ := w.Compile(ctx, engine.CompileRequest{Selector: "pending", Tier: 3})
hits, _ := w.Search(ctx, engine.SearchRequest{Query: "attention", Limit: 5})
```

`pkg/engine` is the only supported surface (facade over `internal/`):
exclusive workspace lock (read-write `Open` fails fast with `ErrLocked`;
`WithReadOnly` for lock-free reads), v0.2.x workspaces open read-only
until adopted via `WithUpgrade`, and no `internal/` type leaks into
exported signatures (lint-tested). Companion packages: `pkg/provider`
(LLM/embedding abstraction + `providerfake` for offline tests),
`pkg/events`, `pkg/mirror`. A full offline example lives in
[`examples/embed`](examples/embed/main.go) and runs in CI. The `compile`,
`search`, `capture`, and `query` commands themselves route through
`pkg/engine`. API stability note: pre-1.0, pin a version.

### Cost

sage-wiki tracks token usage and estimates cost for every compile.
**Prompt caching** (default on) reuses system prompts across calls within
a compile pass — Anthropic and Gemini cache explicitly, OpenAI caches
automatically — saving 50-90% on input tokens. **Batch API**
(Anthropic, OpenAI, and Gemini) halves cost for large compiles:

```bash
sage-wiki compile --batch       # submit batch, checkpoint, exit
sage-wiki compile               # poll status, retrieve when done
```

`compile --estimate` previews cost; `compiler.mode: auto` batches
automatically past a threshold. Prices come from a `provider:model`
registry (embedded estimates → `~/.sage-wiki/prices.json` →
`compiler.price_table`); models with no entry report **unknown**, never a
guessed number. Audit and review spend:

```bash
sage-wiki cost models     # which price produced a number, and its source
sage-wiki cost report     # recorded spend by model and pass/tier
```

Details: [Configuration](docs/guides/configuration.md).

### Resource limits

A single `limits:` block caps every ingestion, compile, query, and serve
surface. Zero (unset) values resolve to the defaults below — a zero never
means "disabled". Every violation fails fast with a typed error and emits a
`limit_exceeded` event. Threat model and residual risks:
[Security](docs/security.md).

```yaml
limits:
  max_doc_bytes: 10485760                  # 10 MiB — max size of one ingested doc
  max_docs_per_capture_batch: 10           # max docs per capture batch
  max_compile_batch: 1000                  # max docs per compile run
  max_query_bytes: 32768                   # 32 KiB — max question length
  max_graph_traversal_nodes: 10000         # max nodes per graph traversal
  max_concurrent_provider_calls: 20        # concurrent LLM/embed calls in a compile
  max_concurrent_requests_per_conn: 8      # serve per-connection in-flight cap
  provider_timeout: 120s                   # per-call LLM/embed deadline
  compile_doc_timeout: 15m                 # per-doc compile budget
```

Serve mode is additionally hardened at the HTTP layer: request/header/idle
timeouts, a 1 MiB request-header cap, a per-connection in-flight request
guard (429 on breach), and a 1 MiB body cap on `/mcp`. An operator-owned
rate-limit middleware slot (`serve.Config.RateLimit`, token-bucket example
included) is the hook for deployment-level policy.

### Scaling to large vaults

Tiered compilation routes each source by type and usage instead of
LLM-compiling everything:

| Tier | What happens | Cost | Time per doc |
|------|-------------|------|-------------|
| **0** — Index only | FTS5 full-text search | Free | ~5ms |
| **1** — Index + embed | FTS5 + vector embedding | ~$0.00002 | ~200ms |
| **2** — Code parse | Structural summary via regex parser (no LLM) | Free | ~10ms |
| **3** — Full compile | Summarize + extract concepts + write articles | ~$0.05-0.15 | ~5-8 min |

For large vaults: index everything at Tier 1 (a 100K-doc vault in ~5.5
hours), then compile on demand — auto-promotion, backpressure, and code parsers are covered in
[Large Vault Performance](docs/guides/large-vault-performance.md).

### Bounded vector memory (opt-in)

By default every search loads the full vector matrix into memory per open
workspace (`vectors.backend: memory`). For very large vaults — or many
workspaces in one process — an on-disk, mmap-served index keeps resident
memory bounded instead:

```yaml
vectors:
  backend: mmap        # memory (default) | mmap
  quantization: none   # none (default, fp32 exact) | int8 (4x smaller)
```

Then build the index once (and after any compile/re-embed — a stale
snapshot falls back to the in-memory cache with a warning, never to wrong
results):

```
sage-wiki index rebuild-vectors
```

- fp32 (`quantization: none`) returns results **identical** to the
  in-memory backend (verified against the golden parity corpus).
- int8 is 4x smaller with a measured recall trade-off (recall@10 = 0.994
  on the reference fixture; the gate is ≥ 0.95).
- The memory ceiling is **unix-only** (real `mmap`); other platforms serve
  the index from memory and warn once.
- Measured on a 50K×384 fixture: search heap 1.9 MB (mmap) vs 86.8 MB
  (memory) — ~2% resident; warm latency within 1.1x; cold search ~6x
  faster (no full cache load).

### Multi-workspace serve

One process can serve many workspaces (personal/work/project vaults —
and the seam a hosting setup composes around; this repo stays
workspace-scoped, with no tenant/quota concepts):

```
sage-wiki serve --workspace-root /path/to/vaults --addr 127.0.0.1:8484
```

- `GET /v1/workspaces` lists the registry (subdirectories of the root
  containing a valid workspace).
- `/w/{name}/...` serves the full per-workspace surface (REST + MCP at
  `/w/{name}/mcp`). Workspace names are validated
  (`[a-z0-9][a-z0-9-_]{0,63}`) — invalid or unknown names get the same
  404.
- Workspaces open lazily; `--max-concurrent-compiles` bounds compiles
  **across** all workspaces (one shared gate). Live workspaces are
  LRU-bounded by the engine Manager (`engine.WithMaxOpen` for API users).
- Auth: the root `--token`/`--token-file` guards all `/w/*` routes.
  Per-workspace auth is a non-goal — compose it in front (reverse proxy)
  if you need it.
- `--workspace-root` is HTTP-only and cannot be combined with `--ui`,
  `--workspace`, or `--transport`.
- Memory note: a served workspace opens up to 3 handles (engine, worker,
  MCP); the bounded-memory goal in multi-workspace serve requires
  `vectors.backend: mmap`.

### Remote mirror (S3 backup)

Continuous, crash-safe replication of a workspace to any S3-compatible
bucket (S3, R2, MinIO) and fast restore from it. The local directory stays
the operating surface — the mirror is durability and mobility, never a
live query path.

```bash
# 1. Configure the mirror: block (below), then:
sage-wiki mirror enable        # validates creds, writes manifest, bootstraps generation 1
sage-wiki mirror status        # local + remote state, pending changes, lag
sage-wiki mirror snapshot      # force a new generation
sage-wiki mirror verify        # full re-hash invariant check (--fast for HEAD-only)
sage-wiki hydrate s3://bucket/prefix /path/to/empty-dir
```

How it ships: the `serve` process ships continuously (`ship_interval`);
every CLI command runs a best-effort ship pass after it finishes (success
or error). The db ships Litestream-style (snapshot + WAL segments);
markdown, prompts, sources, manifests, and vector indexes ship as
content-addressed objects. Crash safety: the commit pointer is written
last, so any kill leaves the previous committed state restorable —
`mirror verify` proves it (full re-download re-hash; `--fast` is
existence-only). Point-in-time restore: `hydrate --at 2026-08-01T12:00:00Z`
(segment granularity for the db; overshoot printed);
`--generation N` pins a generation; `--partial` restores in order
(manifest → db → markdown → vectors) so lexical/graph works before
vectors finish. **PITR scope:** the db restores at the selected
generation/segment; **markdown and source objects restore from the same
generation's sealed object map** (rotation points seal each generation's
doc set into its meta.json) — so a rotated-generation restore is a
consistent tree, not db@TIME with docs@newest. Docs restore at
per-generation granularity: a mid-generation delete may still be
present, a mid-generation create may be missing (bounded, and the
restore report prints both skews). Mirrors written before object maps
fall back to docs@newest with a printed note.

Credentials come from the environment (names configurable), never the
workspace or config values:

```yaml
mirror:
  enabled: false              # `mirror enable` sets this
  endpoint: ""                # e.g. https://<acct>.r2.cloudflarestorage.com or http://localhost:9000
  addressing: "auto"          # auto = virtual-host for amazonaws.com, path-style otherwise; "path"/"virtual" force
  bucket: ""
  prefix: ""                  # default: workspace directory name
  region: "auto"              # SigV4 region; "auto" works for R2/MinIO
  access_key_env: "AWS_ACCESS_KEY_ID"    # NAME of env var, never the value
  secret_key_env: "AWS_SECRET_ACCESS_KEY"
  session_token_env: "AWS_SESSION_TOKEN" # STS session token env var NAME (empty = absent)
  credentials_file: ""        # optional JSON {"access_key","secret_key","session_token"} outside the workspace
  ship_interval: "1s"         # WAL seal cadence while active
  snapshot_interval: "1h"     # scheduled generation cadence
  min_rotation_interval: "60s" # debounce for fold-forced rotations
  ship_lock_timeout: "5s"     # ship-mutex wait for CLI passes
  drain_timeout: "10s"        # serve shutdown budget for the final ship pass
  retain_generations: 2       # PITR depth in ROTATION COUNT, not time — raise for PITR-heavy use
  max_consecutive_defers: 10  # busy-writer deferrals before status surfaces rotation_deferred
  encryption:
    enabled: false            # AES-256-GCM client-side encryption
    key_file: ""              # 32-byte key file — MUST live outside the workspace
```

Notes:

- `config.yaml` itself is **not** mirrored (it can hold secrets like
  `api.api_key`) — hydrate restores data only; run `sage-wiki init` or
  restore your own config after a migration.
- Optional encryption is AES-256-GCM with a keyfile; `mirror verify`
  works **without** the key (integrity hashes cover shipped ciphertext).
- The standalone `sage-wiki tui` has no in-process shipper — its changes
  ship at TUI exit (kill -9 → at the next command). `serve` and
  `serve --ui` ship continuously.
- RPO: with defaults, induced loss loses ≤ `ship_interval` of writes
  (measured: 30.8ms of writes at a 50ms interval — see the test
  `TestRPO_ServeShipper`).


## Ecosystem

### Contribution Packs

Packs bundle ontology types, prompts, and skill triggers for a domain.
Eight bundled packs work offline:

| Pack | Audience | Key ontology |
|------|----------|-------------|
| `academic-research` | Researchers | cites, contradicts, finding, research_hypothesis |
| `software-engineering` | Dev teams | implements, depends_on, adr, runbook |
| `product-management` | PMs | addresses, prioritizes, user_story |
| `personal-knowledge` | Note-takers | relates_to, inspired_by, fleeting_note |
| `study-group` | Students | explains, prerequisite_of, definition |
| `meeting-organizer` | Managers | decided, assigned_to, action_item |
| `content-creation` | Writers | references, revises, draft, published |
| `legal-compliance` | Legal teams | regulates, supersedes, policy, control |

`sage-wiki init --pack academic-research` applies one at init;
`pack install <name|url>` adds more. Creating and publishing packs:
[CONTRIBUTING](CONTRIBUTING.md).

### External Parsers

Handle any file format with a script in any language (stdin → text on
stdout), declared in `parsers/parser.yaml` behind a double opt-in — they
run as unsandboxed subprocesses with timeout enforcement and environment
stripping. Authoring and hardening details: [CONTRIBUTING](CONTRIBUTING.md);
the trust-boundary discussion: [Team Setup](docs/guides/team-setup.md).

### Teams

Three sharing patterns — git-synced, shared server, hub federation — plus
team trust review and cost management: [Team Setup](docs/guides/team-setup.md).

## Benchmarks

Two suites answer different questions. Full detail:
[eval/benchmarks/REPORT.md](eval/benchmarks/REPORT.md) and
[eval/REPORT.md](eval/REPORT.md).

**Memory benchmarks** — can it answer questions about a long conversation?
Published datasets, LLM-judged, using the prompts and procedure from
[mem0ai/memory-benchmarks](https://github.com/mem0ai/memory-benchmarks) with
sage-wiki as the backend (gpt-5 answerer/judge, scoped samples):

| Benchmark | Score | Mem0 Platform (published) |
|---|---|---|
| LOCOMO (150 q) | **92.0%** @ top-50 | 91.8% @ top-50 |
| LongMemEval-S (30 q) | **93.3%** @ top-50 | 94.8% @ top-50 |
| BEAM 100K (60 q) | **0.691** mean nugget | 0.641 @ 1M bucket |

Not a like-for-like ranking: mem0 runs their managed platform on full question
sets, these are scoped samples (±4–5pp), and the compile pipelines differ. The
caveats are spelled out in the report.

**Quality + performance eval** — is the wiki well-formed and fast? Runs on any
compiled wiki, no API keys, seconds. Median across 10 real wikis: overall
**87.4%**, fact extraction 100%, search recall@10 100%, cross-reference
integrity 100%. In-process retrieval: FTS5 top-10 **0.035 ms**, hybrid RRF
**4.9 ms**, graph BFS **0.001 ms**.

```bash
python3 eval/eval.py .                      # quality + perf on your wiki
python3 -m pytest eval/eval_test.py -q      # harness self-tests
```

## Architecture

![Sage-Wiki Architecture](assets/sage-wiki-architecture.png)

- **Storage:** SQLite with FTS5 (BM25 search) + BLOB vectors (cosine similarity) + compile_items table for per-source tier/state tracking
- **Ontology:** Typed entity-relation graph with BFS traversal and cycle detection
- **Search:** Unified pipeline — document- and chunk-level FTS5 and vectors fused by weighted RRF with the ontology graph as a third channel, corpus-adaptive stopwording, title-proxy column weights, and a recency tie-breaker on documents with a known origin date. LLM query expansion and coverage-gated re-ranking are opt-in per call on the search surfaces and on by default for Q&A, which also gets 4-signal graph context expansion. Search responses signal uncompiled sources for compile-on-demand.
- **Compiler:** Tiered pipeline (Tier 0: index, Tier 1: embed, Tier 2: code parse, Tier 3: full LLM compile) with adaptive backpressure, concurrent Pass 2 extraction, prompt caching, batch API (Anthropic + OpenAI + Gemini), cost tracking, compile-on-demand via MCP, quality scoring, and cascade awareness. Embedding includes retry with exponential backoff, optional rate limiting, and mean-pooling for long inputs. 10 built-in code parsers (Go via go/ast, 8 languages via regex, structured data key extraction).
- **MCP:** 19 tools (7 read, 9 write, 3 compound) via stdio or SSE, including `wiki_graph_query` for provenance-cited multi-hop graph QA, `wiki_compile_topic` for on-demand compilation and `wiki_capture` for knowledge extraction
- **TUI:** bubbletea + glamour 4-tab terminal dashboard (browse, search, Q&A, compile) with tier distribution display
- **Web UI:** Preact + Tailwind CSS embedded via `go:embed` with build tag (`-tags webui`)
- **Scribe:** Extensible interface for ingesting knowledge from conversations. Session scribe processes Claude Code JSONL transcripts.
- **Packs:** Contribution pack system with 8 bundled packs, Git-based registry, install/apply/remove/update lifecycle, transactional apply with snapshot rollback, fill-only merge, and config allowlist security.
- **External Parsers:** Runtime-pluggable file format parsers via stdin/stdout subprocess protocol. Sandboxed execution with timeout, env stripping, and network isolation (Linux).

Zero CGO. Pure Go. Cross-platform.

## License

[MIT](LICENSE)
