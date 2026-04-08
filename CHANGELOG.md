# Changelog

## 0.1.1 — 2026-04-08

### Interactive TUI Dashboard

- **`sage-wiki tui`** — New unified terminal dashboard built with bubbletea + lipgloss + glamour, replacing the previous per-command TUI.
- **[F1] Browse** — Navigate articles by section (concepts, summaries, outputs) with glamour-rendered markdown preview.
- **[F2] Search** — Split-pane fuzzy search with hybrid-ranked results and article preview. Enter opens in `$EDITOR`.
- **[F3] Q&A** — Multi-turn conversational Q&A with streaming LLM responses and source citations. Ctrl+S saves answers to outputs/.
- **[F4] Compile** — Live compile dashboard with file list, status icons, and auto-recompile on source changes.
- **Shared component library** — Reusable StatusBar, StreamView, Preview (glamour viewport), and KeyHints components in `internal/tui/components/`.
- **TTY detection** — TUI auto-disabled when piped or in non-interactive shells. All CLI commands still work without a terminal.

### Cost Optimization

- **Cost tracking** — Every compile now prints a cost report showing token usage, estimated cost, and per-pass breakdown. Cached token savings are shown when applicable.
- **Cost estimation** — `compile --estimate` previews cost without compiling, showing standard, batch, and cached pricing.
- **Prompt caching** — Always-on by default. Anthropic uses `cache_control` ephemeral blocks, Gemini uses the `cachedContents` API, OpenAI uses automatic prefix caching. Reduces input token costs by 50-90% on repeated system prompts.
- **Batch API** — `compile --batch` submits sources to the Anthropic or OpenAI batch API for 50% cost reduction. Async workflow: submit → checkpoint → exit, then `compile` again to poll and retrieve results. Handles expiry (24h window) and partial failure gracefully.
- **Auto-batch mode** — Set `compiler.mode: auto` to automatically use the batch API when source count exceeds a threshold (default 10).
- **Interactive estimate prompt** — Set `compiler.estimate_before: true` to show a cost estimate and ask for confirmation before every compile.
- **Cache control** — `compile --no-cache` disables prompt caching for debugging. `compiler.prompt_cache: false` in config to disable permanently.
- **Price override** — `compiler.token_price_per_million` overrides built-in pricing for custom or self-hosted models.
- **TUI integration** — Compile tab status bar shows cost and cache savings after each compile.

### New Config Fields

```yaml
compiler:
  mode: standard          # standard, batch, or auto
  estimate_before: false  # prompt before compiling
  prompt_cache: true      # enable prompt caching (default: true)
  batch_threshold: 10     # min sources for auto-batch
  token_price_per_million: 0  # override pricing (0 = use built-in)
```

### New CLI Flags

- `compile --batch` — Use batch API (async, 50% discount)
- `compile --no-cache` — Disable prompt caching for this run
- `compile --estimate` — Show cost estimate without compiling

### Other Changes

- Default Gemini model updated from `gemini-2.0-flash` to `gemini-2.5-flash`.
- `sage-wiki init --model` flag added to specify model during setup.

### Fixes

- Fixed potential infinite recursion when cached LLM requests fail and fall back to standard path.
- Gemini cached requests no longer send duplicate `systemInstruction` alongside `cachedContent`.
- Batch API responses validated against pending source list before processing.
- Checkpoint save errors properly handled after batch submission.
- HTTP timeouts (120s) added to all batch API calls.
- Malformed JSONL lines in batch results now logged instead of silently skipped.

## 0.1.0 — 2026-04-07

First public release of sage-wiki, an LLM-compiled personal knowledge base.

### Core

- **5-pass compiler pipeline** — diff detection, summarization, concept extraction, article writing, and image captioning. Supports parallel LLM calls with checkpoint/resume.
- **Multi-format source extraction** — Markdown, PDF, Word (.docx), Excel (.xlsx), PowerPoint (.pptx), CSV, EPUB, email (.eml), plain text, transcripts (.vtt/.srt), images (via vision LLM), and code files.
- **Hybrid search** — Reciprocal Rank Fusion combining BM25 (FTS5) + cosine vector similarity + tag boost + recency decay.
- **Ontology graph** — Typed entity-relation graph with BFS traversal, cycle detection, and concept interlinking via `[[wikilinks]]`.
- **Q&A agent** — Natural language questions answered with LLM synthesis, source citations, and auto-filed output articles.
- **Watch mode** — File system watcher with debounce, polling fallback for WSL2/network drives.

### LLM Support

- **Providers** — Anthropic, OpenAI, Gemini, Ollama, and any OpenAI-compatible API (OpenRouter, Azure, etc.).
- **Streaming** — Native SSE streaming for all providers (OpenAI, Anthropic, Gemini).
- **Per-task model routing** — Configure different models for summarize, extract, write, lint, and query tasks.
- **Embedding cascade** — Provider API embeddings with Ollama fallback. Auto-detect dimensions for unknown models.
- **Rate limiting** — Token bucket rate limiter with exponential backoff on 429s.

### Web UI

- **Article browser** — Rendered markdown with syntax highlighting, clickable `[[wikilinks]]`, frontmatter badges, and breadcrumb navigation.
- **Knowledge graph** — Interactive force-directed visualization with node coloring by type, neighborhood queries, and click-to-navigate.
- **Streaming Q&A** — Ask questions in the browser with real-time token streaming and source citations. Answers auto-filed to outputs/.
- **Search** — Debounced hybrid search with ranked results and snippets.
- **Table of contents** — Scroll-spy with active heading highlight, toggleable with graph view.
- **Dark/light mode** — Toggle with system preference detection and localStorage persistence.
- **Broken link detection** — Missing article links shown in gray with tooltip.
- **Hot reload** — WebSocket-based auto-refresh when wiki files change (pairs with `compile --watch`).
- **Keyboard shortcuts** — `/` focuses search, `Esc` clears.
- **Embedded in binary** — Preact + Tailwind CSS via `go:embed` with build tag. Binary works without web UI when built without `-tags webui`.

### MCP Server

- **14 tools** — 5 read (search, read, status, graph, list), 7 write (add source, write summary, write article, add ontology, learn, commit, compile diff), 2 compound (compile, lint).
- **Transports** — stdio (for Claude Code, Cursor, etc.) and SSE (for network clients).
- **Path traversal protection** — All file operations validated with `isSubpath`.

### CLI

- `sage-wiki init [--vault] [--prompts]` — Greenfield or Obsidian vault overlay setup.
- `sage-wiki compile [--watch] [--dry-run] [--fresh] [--re-embed] [--re-extract]` — Full compiler with multiple modes.
- `sage-wiki serve [--ui] [--transport stdio|sse] [--port] [--bind]` — MCP server or web UI.
- `sage-wiki search`, `query`, `ingest`, `lint`, `status`, `doctor` — Full CLI toolkit.
- **Customizable prompts** — `sage-wiki init --prompts` scaffolds editable prompt templates.

### Linting

- **7 passes** — Completeness, style (with auto-fix), orphans, consistency, connections, impute, and staleness.
- **Learning integration** — Dedup via SHA-256, 500 cap, 180-day TTL, keyword recall.

### Quality

- Zero CGO. Pure Go. Single binary. Cross-platform (Linux, macOS, Windows — amd64 + arm64).
- SQLite with WAL + single-writer mutex for concurrent safety.
- CSRF protection, SSRF validation, request body limits, file type allowlists.
- 20 test packages, all passing.

### Binaries

| Platform | Binary |
|----------|--------|
| Linux amd64 | `sage-wiki-linux-amd64` |
| Linux arm64 | `sage-wiki-linux-arm64` |
| macOS amd64 (Intel) | `sage-wiki-darwin-amd64` |
| macOS arm64 (Apple Silicon) | `sage-wiki-darwin-arm64` |
| Windows amd64 | `sage-wiki-windows-amd64.exe` |
| Windows arm64 | `sage-wiki-windows-arm64.exe` |
