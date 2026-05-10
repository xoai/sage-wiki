# Changelog

## 0.1.9 — 2026-05-10

### Contribution Packs

Installable configuration profiles that bundle ontology types, prompts, and sample sources for specific domains. Packs are composable, versioned, and work offline.

- **8 bundled packs** — `academic-research`, `software-engineering`, `product-management`, `personal-knowledge`, `study-group`, `meeting-organizer`, `content-creation`, `legal-compliance`. Embedded in the binary via `go:embed`, available offline.
- **Pack lifecycle** — `pack install` (from local path, Git URL, registry, or bundled), `pack apply` (transactional with snapshot rollback), `pack remove` (restores pre-apply state), `pack update` (per-file diff with conflict detection).
- **Git-based registry** — [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs) repository with `index.yaml`. `pack search` queries the registry. Stale cache fallback on network failure.
- **Pack authoring** — `pack create` scaffolds a new pack. `pack validate` checks schema, paths, ontology names, and config overlay safety. `pack conflicts` shows multi-pack file overlaps.
- **Init integration** — `sage-wiki init --pack <name>` installs and applies a contribution pack during project setup. Uses replace mode for new projects, merge mode for existing ones.
- **Fill-only merge** — Pack config overlays use fill-only semantics: pack values apply only where the project has no value. User config is never silently overwritten.
- **Config allowlist** — Only safe config keys (compiler, search, linting, ontology, trust, type_signals, ignore) are allowed in pack overlays. Keys like api, embed, models, parsers, serve are stripped to prevent credential hijacking.
- **Security hardening** — Path traversal protection (ValidateRelPath + symlink resolution), atomic cache replacement, transactional state persistence, source boundary enforcement (registry-only updates), parser opt-in via `--enable-parsers`.

### External Parsers

Runtime-pluggable file format parsers via stdin/stdout subprocess protocol. Add support for any file format by writing a parser script in any language.

- **Stdin protocol** — sage-wiki pipes file content to stdin, parser writes plain text to stdout. No filesystem access needed.
- **Sandboxed execution** — Timeout enforcement (30s default, 120s max), environment stripping (only PATH, HOME, LANG), network isolation via `CLONE_NEWNET` on Linux.
- **`parsers/parser.yaml`** — Extension-to-command mapping. Relative script paths resolved against `parsers/` directory.
- **Compiler integration** — External parsers checked after built-in format detection, before plain text fallback. Wired into all 5 Extract() call sites via `ExtractOpts` variadic pattern.
- **Explicit opt-in** — Requires `parsers.external: true` in config. Packs with parsers require `--enable-parsers` flag during apply.

### Skill System Simplification

- **Presets removed** — The 4 domain-specific skill templates (`codebase-memory`, `research-library`, `meeting-notes`, `documentation-curator`) are removed. `sage-wiki init --skill claude-code` now renders a single generic base template with MCP tool guidance, entity types, and relation types from config.yaml.
- **Domain skills in packs** — Domain-specific agent behavior (when to search for papers, how to capture meeting decisions, etc.) now lives in pack `skills/` directories. Apply a pack to get domain-specific triggers alongside the base skill.
- **`--pack` flag simplified** — On `init`, `--pack` always means a contribution pack. No more ambiguity with skill presets. On `skill refresh/preview`, no `--pack` or `--preset` flags needed.

### New Commands

| Command | Description |
|---------|-------------|
| `pack install <name\|url>` | Install a pack from bundled, registry, local, or Git |
| `pack apply <name>` | Apply an installed pack to the project |
| `pack remove <name>` | Remove a pack and restore pre-apply state |
| `pack list` | List applied, cached, and bundled packs |
| `pack search <query>` | Search the pack registry |
| `pack update [name]` | Update packs to latest versions |
| `pack info <name>` | Show pack details |
| `pack create <name>` | Scaffold a new pack directory |
| `pack validate [path]` | Validate pack schema and files |
| `pack conflicts` | Show multi-pack file overlaps |

### Documentation

- **[CONTRIBUTING.md](CONTRIBUTING.md)** — Guide for pack authors and parser contributors. Covers pack.yaml schema, directory structure, testing, registry submission, and external parser authoring.
- **README updated** — Contribution packs section, external parsers section, updated commands table, architecture description.

---

## 0.1.8 — 2026-05-09

### Output Trust System (issue #74)

Query outputs are now treated as claims, not facts. Outputs earn trust through grounding verification and consensus before entering the searchable corpus. This prevents data poisoning from incorrect LLM answers feeding back into future queries.

- **Tri-state trust mode** — `trust.include_outputs` config: `"false"` (default, outputs excluded from search), `"verified"` (only confirmed outputs in search), `"true"` (legacy, all outputs indexed).
- **`sage-wiki verify`** — Run LLM-based grounding checks on pending outputs. Extracts factual claims and checks entailment against source passages. Auto-promotes when both grounding and consensus thresholds are met.
- **Consensus pipeline** — Repeated queries that produce the same answer from independent source chunks build confirmations. Independence is scored via Jaccard distance between chunk sets. Configurable via `trust.consensus_threshold` (default: 3) and `trust.similarity_threshold` (default: 0.85).
- **Conflict detection** — When the same question produces contradictory answers, both are flagged as conflicts. Resolve via `sage-wiki outputs resolve <id>`.
- **`sage-wiki outputs list`** — List outputs by trust state: pending, confirmed, conflict, stale.
- **`sage-wiki outputs promote <id>`** — Manually promote a pending output to confirmed (indexes into FTS5, vectors, ontology, chunks).
- **`sage-wiki outputs reject <id>`** — Reject and delete a pending output (de-indexes and removes file).
- **`sage-wiki outputs resolve <id>`** — Promote one answer and reject all competing answers for the same question.
- **`sage-wiki outputs clean --older-than 90d`** — Remove stale pending outputs older than a threshold.
- **`sage-wiki outputs migrate`** — Migrate existing `wiki/outputs/` files into the trust system. Parses sources from frontmatter.
- **Source change demotion** — During `sage-wiki compile`, confirmed outputs are automatically demoted to stale when their cited source files change. Only runs in `"verified"` mode.
- **Pending output quarantine** — New query outputs are written to `wiki/under_review/` with state frontmatter. Promoted outputs move to `wiki/outputs/` and are indexed.
- **Idempotent confirmations** — Duplicate evidence from the same source chunks is silently skipped. Prevents inflation of confirmation counts.
- **Atomic promotion** — File move and search indexing complete before DB state is marked confirmed. Failures roll back cleanly.

See the [output trust guide](docs/guides/output-trust.md) for configuration, workflows, and architecture.

## 0.1.7 — 2026-05-08

### Subscription Auth (issue #15)

Use your existing LLM subscription (ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot, Google Gemini) instead of managing separate API keys and billing.

- **`sage-wiki auth login --provider openai`** — Browser-based PKCE OAuth flow. Supports OpenAI and Anthropic. Headless fallback for SSH/WSL (paste redirect URL).
- **`sage-wiki auth import --provider claude`** — Import credentials from existing CLI tools (Codex CLI, Claude Code, GitHub Copilot, Gemini CLI).
- **`sage-wiki auth status`** — List stored credentials with masked tokens, source, and expiry status.
- **`sage-wiki auth logout --provider openai`** — Remove stored credentials.
- **`api.auth: subscription`** — New config field. When set, sage-wiki uses subscription credentials instead of `api_key`. Auth precedence: environment variable > subscription > api_key.
- **Auto-refresh** — Tokens refresh transparently during long compiles. Uses RWMutex with double-checked locking to avoid serializing concurrent compiler goroutines.
- **Batch mode auto-disabled** — Subscription tokens cannot access the batch API. Automatically falls back to standard mode with a warning.
- **Global credential store** — Tokens stored at `~/.sage-wiki/auth.json` (0600 permissions). Login once, use across all projects.
- **TOS warning** — Displayed on first login/import. Providers may change terms at any time.

See the [subscription auth guide](docs/guides/subscription-auth.md) for setup, supported providers, and troubleshooting.

### GPT-5.x Support (PR #76)

- **`max_completion_tokens` for GPT-5.x and reasoning models** — OpenAI's GPT-5.x and o1/o3/o4 models reject the legacy `max_tokens` parameter. The OpenAI provider now detects model families and sends the correct parameter. Also fixes a bug where `extra_params` token-limit overrides were silently dropped.

### Config Flag Fix (PR #77)

- **`--config` flag wired to all commands** — The `--config` persistent flag was defined but never read. All 14 command handlers now use the flag via `resolveConfigPath()`. Thanks @Joneyao.

### Streaming Transport Fix

- **Streaming uses client transport** — `ChatCompletionStream` previously created a standalone `http.Client`, bypassing any transport wrapping (subscription auth, metrics, etc.). Now uses the client's configured transport.

## 0.1.6 — 2026-05-04

### Embedding Reliability (PR #68)

- **Retry with exponential backoff** — embedding API calls retry up to 3 times on 429/503 with exponential backoff (1s, 2s, 4s) + jitter. Respects `Retry-After` header.
- **`embed.rate_limit` config** — optional client-side RPM pacing for embedding calls. Default 0 (no limit). Set to e.g. `1200` for Gemini Tier 1.
- **BackpressureController fires for embeddings** — 429 errors now return typed `*RateLimitError`, triggering concurrency halving. Previously dead code.
- **Partial failure recovery** — `PassEmbedded` only marks on full success. Failed chunks retry on next compile without re-processing everything.

### Compiler Performance (PRs #69, #70)

- **Concurrent Pass 2** (#69) — concept extraction batches run in parallel (bounded by `max_parallel`). ~N× speedup for providers with continuous batching (OpenRouter, vLLM, Groq).
- **Rate limiter fix** (#70) — mutex no longer held across sleep. Self-hosted backends (`openai-compatible`, `ollama`) get 0 default RPM (no client-side cap). Shared HTTP transport with `MaxIdleConnsPerHost: 256`.
- **Mean-pooling for long inputs** (#75) — embedder splits inputs >5K runes into chunks and mean-pools. Prevents 413 errors on 8K-token-limited providers (GLM, bge-m3). `re-embed` command also re-processes `vec_chunks`.

### Ontology Quality (PRs #71, #72)

- **Block-level relation extraction** (#71) — keywords must co-occur with `[[wikilinks]]` in the same paragraph/heading block. Eliminates ~90% of spurious edges from cross-paragraph matches.
- **Type-restricted relations** (#72) — optional `valid_sources`/`valid_targets` fields on relation configs. Only creates edges between matching entity types. Backward compatible (empty = all types allowed).

### Multilingual (PR #73)

- **Language in hierarchical synthesis** — the `language` config now applies to hierarchical summary synthesis for multi-chunk documents (was only applied to single-chunk path).

## 0.1.5 — 2026-04-17

### Agent Skill Templates

Agents ignore sage-wiki's 17 MCP tools because nothing tells them *when* to use them. Skill templates are behavioral bridges — generated snippets appended to agent instruction files.

- **`sage-wiki init --skill <agent>`** — Generate a skill file during project init. Supported agents: `claude-code`, `cursor`, `windsurf`, `agents-md`, `codex`, `gemini`, `generic`.
- **`sage-wiki skill refresh`** — Regenerate the skill section on an existing project. Marker-based replacement preserves surrounding content.
- **`sage-wiki skill preview`** — Preview the generated skill without writing files.
- **4 domain packs** — `codebase-memory` (default for code projects), `research-library` (paper/article projects), `meeting-notes`, `documentation-curator`. Auto-selected from source types in config, overridable via `--pack`. *(Superseded in 0.1.9 by contribution packs with domain skills.)*
- **Project-specific content** — Templates reference actual entity types, relation types, and MCP tools from your config.yaml.
- **Safe on existing projects** — Running `init --skill` on an already-initialized project skips project creation and only generates the skill file.

### Compile Options Harmonization

Fixed `--watch --prune` silently dropping `--prune` (GitHub issue #61). All compile options now flow correctly through all 5 entry points.

- **`--watch --prune` works** — Watch mode passes all compile options (`--prune`, `--no-cache`, `--fresh`) to both initial and triggered compiles.
- **`--batch --watch` rejected** — Clear error instead of undefined behavior.
- **`--fresh` under watch** — Applies only to the initial compile; subsequent triggered compiles skip fresh to avoid re-processing the entire wiki on every edit.
- **Pending batch detection** — Watch mode refuses to start when a batch compile is in progress, with an actionable error message.
- **Orphan preservation** — When `--prune` is not set and a source removal would orphan an article, all state mutations are deferred (manifest, memory store, vector store, concept references). A subsequent `--prune` run cleanly removes the orphan. Previously, state was scrubbed immediately, stranding the orphan permanently.
- **MCP `wiki_compile` gets `prune`** — The `prune` argument is now available on the MCP tool.
- **`hub compile --prune`** — New flag on the hub multi-project compile command.
- **TUI plumbing** — CompileOpts threaded through the TUI compile model (UI toggle deferred).

### Gemini Batch API (PR #63)

- **Gemini batch support** — `compile --batch` now works with Gemini provider. 50% cost reduction with separate quota bucket. Uses File API upload for arbitrarily large batches.
- **Configurable concept extraction** — `extract_batch_size` and `extract_max_tokens` in config.yaml to avoid JSON truncation on large corpora.

### Search Quality

- **Cross-lingual vector search (PR #67)** — Vector search now runs brute-force across all chunks instead of being BM25-prefiltered. Fixes cross-lingual queries (e.g., Polish query against English content) where BM25 has zero lexical overlap.
- **Hybrid weight fix (PR #67)** — The `query` command now correctly passes configured `hybrid_weight_bm25` / `hybrid_weight_vector` to the doc-level search path (was defaulting to 1.0/1.0).
- **CJK search fix (PR #65)** — `SanitizeFTS` now preserves CJK ideographs, kana, and hangul. Previously all non-ASCII characters were stripped, causing BM25 to return zero results for Chinese/Japanese/Korean queries.
- **Chunk-level Tier 1 embedding (#66)** — Long sources at Tier 1 are now split into ~800-token chunks before embedding, stored in `vec_chunks` + `chunks_fts`. Eliminates silent truncation for documents exceeding embedding model token limits.

## 0.1.4 — 2026-04-15

### Large Vault Performance

Architecture shift from "compile everything" to "index fast, compile what matters" for vaults of 10K-100K+ documents. 9 milestones across 4 phases, 4 independent code reviews passed.

#### Tiered Compilation

- **4-tier system** — Tier 0 (FTS5 index, ~5ms, free), Tier 1 (+ vector embed, ~200ms), Tier 2 (code parse, ~10ms, free), Tier 3 (full LLM compile, ~5-8 min). A 100K vault is searchable at Tier 1 in ~5.5 hours instead of 555 days.
- **File-type-aware defaults** — JSON/YAML/TOML/lock → Tier 0, prose/code → Tier 1. Configurable via `compiler.tier_defaults`.
- **Per-file overrides** — `.wikitier` files per directory and `tier:` frontmatter field. Priority: frontmatter > .wikitier > tier_defaults > default_tier.
- **Auto-promotion** — Sources promote to Tier 3 after 3+ search hits or when topic cluster reaches 5+ sources. Configurable via `compiler.promote_signals`.
- **Auto-demotion** — Stale articles (90 days without queries) demote to Tier 1. Modified sources revert for recompilation. Configurable via `compiler.demote_signals`.
- **compile_items table** — New SQLite migration V5 with per-source tier, 6 pass-completion flags, promotion/demotion timestamps, quality metrics, and 5 indexes. Replaces JSON `compile-state.json` for checkpoint/resume.
- **Checkpoint migration** — Existing `compile-state.json` auto-migrates to `compile_items` on first compile. Batch-in-flight checkpoints preserved.

#### Compile-on-Demand

- **`wiki_compile_topic` MCP tool** — Agents trigger compilation for specific topics. Searches for uncompiled sources, promotes to Tier 3, runs full pipeline. ~2 min for 20 sources.
- **Search response signaling** — `wiki_search` now returns `uncompiled_sources` count and `compile_hint` in every response. Agents know when richer results are available.
- **CompileCoordinator** — Serializes background (watch mode) and on-demand compiles via shared mutex with `TryCompile` (non-blocking) and `CompileOrWait` (context-aware timeout).

#### Adaptive Backpressure

- **Default `max_parallel` 4→20** — Safe for all paid API tiers.
- **BackpressureController** — Replaces fixed semaphore. Halves concurrency on 429s with exponential backoff + jitter. Doubles back after 5 consecutive successes. Self-tunes to any provider's rate limits at runtime.
- **RateLimitError type** — LLM client detects HTTP 429 across all providers and returns typed error for backpressure integration.

#### Code Parsers

- **10 built-in parsers** — Go (via `go/parser` + `go/ast`, perfect accuracy), TypeScript/JavaScript, Python, Rust, Java, C/C++, Ruby (via regex, ~90% coverage), JSON/YAML/TOML (key extraction).
- **Pluggable `Parser` interface** — `internal/extract/parsers/` package with Registry. Future tree-sitter WASM upgrade path.
- **Pipeline integration** — Structural summaries appended to FTS5 entries at Tier 0/1. Code searchable by function name, type, import path.

#### Document Splitting

- **`SplitByHeadings()`** — Splits large documents (>15K chars) at markdown heading boundaries for the write pass. Reduces context per LLM call by 3-4x.
- **Section-aware article writing** — `buildSourceContext()` selects only sections relevant to each concept via term matching. 4K char cap per source.

#### Quality Scoring

- **Per-article confidence** — Source coverage (40%), extraction completeness (30%), cross-reference density (30%). Stored in `compile_items.quality_score`.
- **QualityPass in linter** — `sage-wiki lint` flags articles below quality threshold (default 0.5). Reports tier distribution and compilation error count.
- **`source_type` tracking** — Distinguishes compiler/scribe/manual ingestion paths in compile_items.

#### Concept Deduplication

- **Embedding-based dedup cache** — Cosine similarity check before article writing (threshold 0.85). Near-duplicate concepts merge as aliases. Capped at 50K entries, loads existing vectors from store (no re-embedding on seed).

#### Session Scribe

- **Scribe interface** — `internal/scribe/` package with pluggable `Scribe` interface (Name, Process → Result). Extensible for future git-commit and issue-tracker scribes.
- **Session scribe** — Processes Claude Code JSONL transcripts: compress (strip thinking blocks, ~99% reduction) → extract entities via LLM (max 10/session, kebab-case ID gate) → compare against ontology (ADD/UPDATE/NONE disposition). Handles both string and array-of-blocks content formats.
- **`sage-wiki scribe <file>`** — New CLI command for session entity extraction.

#### Batch API Default

- **`mode: auto`** is now the default. Automatically uses batch API (50% cost savings) when 10+ sources are pending and the provider supports it.

### New Config Fields

```yaml
compiler:
  max_parallel: 20              # adaptive backpressure (was 4)
  mode: auto                    # standard, batch, or auto
  default_tier: 3               # 0=index, 1=embed, 3=compile
  tier_defaults:                # per-extension tier overrides
    json: 0
    yaml: 0
    md: 1
    go: 1
  auto_promote: true
  promote_signals:
    query_hit_count: 3
    cluster_size: 5
    import_centrality: 10
  auto_demote: true
  demote_signals:
    source_modified: true
    stale_days: 90
  split_threshold: 15000        # chars, for document splitting
  backpressure: true
  dedup_threshold: 0.85         # cosine similarity for concept dedup
```

### New Commands

- `sage-wiki scribe <session-file>` — Extract entities from session transcripts

### New MCP Tools

- `wiki_compile_topic(topic, max_sources?)` — Compile sources for a specific topic on demand

### Documentation

- **[Scaling guide](docs/guides/large-vault-performance.md)** — Comprehensive guide covering tiers, config, on-demand compilation, backpressure, code parsers, quality scoring, cost estimation, and recommended workflow for large vaults.
- **[Local models guide](docs/guides/local-models.md)** — Per-pass model routing, GPU/CPU/mixed configurations, quality trade-offs, Ollama setup.

### Stats

- 27 packages, 0 failures
- 64 files changed, 7,708 insertions
- 4 ADRs (023-026)
- 4 independent code reviews passed

### Binaries

| Platform                    | Binary                        | Size  |
| --------------------------- | ----------------------------- | ----- |
| Linux amd64                 | `sage-wiki-linux-amd64`       | 33 MB |
| Linux arm64                 | `sage-wiki-linux-arm64`       | 31 MB |
| macOS amd64 (Intel)         | `sage-wiki-darwin-amd64`      | 34 MB |
| macOS arm64 (Apple Silicon) | `sage-wiki-darwin-arm64`      | 33 MB |
| Windows amd64               | `sage-wiki-windows-amd64.exe` | 34 MB |
| Windows arm64               | `sage-wiki-windows-arm64.exe` | 32 MB |

### Docker

```bash
docker pull ghcr.io/xoai/sage-wiki:v0.1.4
docker pull xoai/sage-wiki:v0.1.4
```

---

## 0.1.3 — 2026-04-11

### Graph-Enhanced Retrieval

- **4-signal graph relevance scorer** — New `internal/graph/` package scores candidate articles using four signals: direct ontology relations (×3.0), shared source documents via `cites` edges (×4.0), Adamic-Adar common neighbors (×1.5), and entity type affinity (×1.0). Uses only the SQLite ontology store — no manifest loading at query time.
- **Graph-expanded context** — After hybrid search, the graph scorer finds related articles missed by keyword/vector search and adds them to the LLM synthesis context. Applied as post-processing in `buildQueryContext()` so both enhanced (chunk-level) and document-level search paths benefit.
- **Token budget control** — Query context capped at configurable `context_max_tokens` (default 8000). Articles truncated at 4000 tokens each (chars/4 estimation). Greedy filling from highest-scored down.

### Source Provenance

- **CLI `sage-wiki provenance`** — Given a source path, shows all generated articles. Given a concept name, shows contributing sources. Auto-detects direction.
- **MCP `wiki_provenance` tool** — Parameters: `source` or `article`. Returns JSON provenance mapping. Registered in read tools and CallTool dispatch.
- **Web API `GET /api/provenance`** — Query params `?source=path` or `?article=name`. Loads manifest from disk for each request.
- **Manifest helpers** — `ArticlesFromSource(path)` reverse-lookup (O(n) scan, fine for typical wikis) and `SourcesForArticle(name)` direct lookup.

### Cascade Awareness

- **Orphan detection on source removal** — When a source is removed during compile, affected concepts are identified _before_ the manifest entry is deleted. Single-source concepts are flagged as orphaned with a log warning. Multi-source concepts get their sources list updated.
- **`--prune` flag** — Opt-in destructive cleanup: `sage-wiki compile --prune` deletes orphaned article files, removes FTS5/vector/ontology entries, and cleans up the manifest. Warn-only by default.

### Ontology Helpers

- **`EntityDegree(id)`** — Returns total relation count (inbound + outbound) for an entity. Used by Adamic-Adar scoring.
- **`EntitiesCiting(targetID)`** — Reverse `cites` lookup: finds all concepts that cite a source entity.
- **`CitedBy(entityID)`** — Forward `cites` lookup: finds all source entities that a concept cites.

### New Config Fields

```yaml
search:
  graph_expansion: true # enable graph-based context expansion (default: true)
  graph_max_expand: 10 # max articles added via graph
  graph_depth: 2 # ontology traversal depth
  context_max_tokens: 8000 # token budget for query context
  weight_direct_link: 3.0 # graph signal weights
  weight_source_overlap: 4.0
  weight_common_neighbor: 1.5
  weight_type_affinity: 1.0
```

All fields optional with sensible defaults. `graph_expansion` uses `*bool` pattern (like `query_expansion`, `rerank`) — nil defaults to true. Existing configs work unchanged.

---

## 0.1.2 — 2026-04-10

### Docker & Self-Hosting

- **Dockerfile** — Multi-stage build (Node + Go + Alpine) with web UI embedded. Runs as non-root user (UID 1000). ~24MB binary on Alpine base.
- **Docker CI** — GitHub Actions workflow builds multi-arch images (`linux/amd64` + `linux/arm64`) and pushes to both GHCR (`ghcr.io/xoai/sage-wiki`) and Docker Hub (`xoai/sage-wiki`) on push to `main` and version tags.
- **Self-hosting guide** — Comprehensive guide at `docs/guides/self-hosted-server.md` covering Docker Compose, Syncthing-based sync, LLM provider config (including OpenAI-compatible with custom `base_url`, local Ollama/vLLM), reverse proxy with HTTPS, VPS deployment, and Raspberry Pi/ARM.

### Configurable Ontology Relations

- **`ontology.relations` config section** — Extend built-in relation types with additional synonyms (e.g., multilingual keywords) or add custom domain-specific relation types. Relation names validated at config load (`^[a-z][a-z0-9_]*$`).
- **Two-tier merge** — 8 built-in types always present; config entries either append synonyms to a built-in or create a new type.
- **Application-layer validation** — SQL CHECK constraint replaced with `AddRelation()` validation from merged config. All 12 `NewStore` call sites updated.
- **DB migration** — `migrationV2` automatically removes the CHECK constraint from existing databases on first open.
- **Guide** — `docs/guides/configurable-relations.md` with domain examples (biology, software architecture, humanities) and built-in synonym tables.

### New Config Fields

```yaml
ontology:
  relations:
    - name: implements
      synonyms: ["thực hiện", "triển khai"] # extend built-in with multilingual synonyms
    - name: regulates
      synonyms: ["regulates", "regulated by"] # add a custom relation type
```

### Fixes

- **Chunk synthesis for large sources** — Files with 60+ chunks no longer fail. Enforces minimum 200-token per-chunk budget with automatic chunk grouping. Hierarchical synthesis reduces summaries in tiers of 8 instead of one flat pass, enabling 1000+ page documents. Empty LLM responses now treated as errors instead of silent propagation. (#20)
- **CJK-aware token estimation** — Token estimator now counts CJK characters (Han, Hangul, Katakana, Hiragana) at 1.5 tokens/char instead of flat 4 chars/token, fixing 2.5x underestimate for Chinese/Japanese/Korean text. Affects chunking accuracy for all CJK-heavy documents.
- **Custom prompts in `--re-extract`** — `ReExtract()` now loads prompt overrides from `prompts/` directory, matching the main `Compile()` path. (#23)
- **Duplicate frontmatter** — Eliminated duplicate YAML frontmatter in generated articles when LLM response already contains frontmatter.
- **`<think>` tag stripping** — LLM responses containing `<think>...</think>` reasoning tags (common with DeepSeek, QwQ) are now stripped across all code paths.
- **Prompt template wiring** — Pass 2 (concept extraction) and Pass 3 (article writing) now use `prompts.Render()` for custom prompt overrides instead of hardcoded strings.
- **Timezone support** — `compiler.timezone` config option for user-facing timestamps in generated frontmatter (IANA format, e.g., `Asia/Shanghai`).

### Community Contributions

- Chinese keywords for ontology relation extraction (@kailunguu-code, #11)
- Vector search wired into hybrid search for MCP and CLI (@kailunguu-code, #9)
- UTF-8 safe concept name formatting for CJK characters (@kailunguu-code, #8)

### Binaries

| Platform                    | Binary                        |
| --------------------------- | ----------------------------- |
| Linux amd64                 | `sage-wiki-linux-amd64`       |
| Linux arm64                 | `sage-wiki-linux-arm64`       |
| macOS amd64 (Intel)         | `sage-wiki-darwin-amd64`      |
| macOS arm64 (Apple Silicon) | `sage-wiki-darwin-arm64`      |
| Windows amd64               | `sage-wiki-windows-amd64.exe` |
| Windows arm64               | `sage-wiki-windows-arm64.exe` |

### Docker

```bash
docker pull ghcr.io/xoai/sage-wiki:v0.1.2
docker pull xoai/sage-wiki:v0.1.2
```

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
  mode: standard # standard, batch, or auto
  estimate_before: false # prompt before compiling
  prompt_cache: true # enable prompt caching (default: true)
  batch_threshold: 10 # min sources for auto-batch
  token_price_per_million: 0 # override pricing (0 = use built-in)
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

| Platform                    | Binary                        |
| --------------------------- | ----------------------------- |
| Linux amd64                 | `sage-wiki-linux-amd64`       |
| Linux arm64                 | `sage-wiki-linux-arm64`       |
| macOS amd64 (Intel)         | `sage-wiki-darwin-amd64`      |
| macOS arm64 (Apple Silicon) | `sage-wiki-darwin-arm64`      |
| Windows amd64               | `sage-wiki-windows-amd64.exe` |
| Windows arm64               | `sage-wiki-windows-arm64.exe` |
