# Configuration Reference

The complete annotated `config.yaml`, plus the setups that build on it.
Blocks that have an owning deep-dive guide are annotated inline here and
covered in depth there — this page is the one place every key appears:

- trust → [output-trust.md](output-trust.md)
- search → [search-quality.md](search-quality.md)
- ontology graph passes → [graph-memory.md](graph-memory.md)
- relation/entity types → [configurable-relations.md](configurable-relations.md)
- storage → [storage-backends.md](storage-backends.md)
- prompts + article_fields → [customizing-prompts.md](customizing-prompts.md)

## Full annotated example

Every key below is optional beyond the provider/model basics; defaults
shown are illustrative (a fresh `sage-wiki init` writes `gemini-2.5-flash`
models and `max_parallel: 4`).

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

# Output language for generated articles (default: English). When set, the
# article body, H1 title, and section headings are all written in this
# language; code, identifiers, proper nouns, and [[wikilink]] targets are kept
# in their original form. Use the language's own name, e.g. 简体中文, 日本語.
# language: 简体中文

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
                                # requires: sage-wiki auth login (openai, anthropic)
                                # or: sage-wiki auth import (gemini — import-only)
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

# Multi-provider setups (separate api/embed providers, keys, rate
# limits): see the Multi-Provider Setup section below.

compiler:
  max_parallel: 4 # concurrent LLM calls (with adaptive backpressure)
  debounce_seconds: 2 # watch mode debounce
  summary_max_tokens: 4000
  article_max_tokens: 4000
  # temperature: 0            # compile sampling temperature 0-2 (default: explicit 0 —
                             # the deterministic setting; anything higher makes artifacts
                             # non-reproducible, see docs/determinism.md)
  # extract_batch_size: 20     # summaries per concept-extraction call (reduce to avoid JSON truncation on large corpora)
  # extract_max_tokens: 8192   # max output tokens for concept extraction (increase to 16384 if extraction is truncating)
  auto_commit: true # git commit after compile
  auto_lint: true # run lint after compile
  mode: auto # standard, batch, or auto (auto = batch when 10+ sources)
  # estimate_before: false    # prompt with cost estimate before compiling
  # prompt_cache: true        # enable prompt caching (default: true)
  # batch_threshold: 10       # min sources for auto-batch mode
  # min_concept_sources: 1    # DEFAULT — concepts with fewer declared sources get no
                              # article/entity/manifest entry (issue #128); 0 disables.
                              # Concept dedup also merges normalized alias overlaps;
                              # when two merge, the LONGER name stays canonical.
  # token_price_per_million: 0  # override pricing (0 = use built-in)
  # timezone: Asia/Shanghai   # IANA timezone for user-facing timestamps (default: UTC)
  # quality:                  # zero-LLM article quality scorer (advisory, no gate)
  #   threshold: 0.5          # warn when an article's composite score is below this
  #   weight_format: 0.15     # 5 dimensions: format / grounding / coverage / wikilink / antipattern
  #   weight_grounding: 0.30
  #   weight_coverage: 0.20
  #   weight_wikilink: 0.15
  #   weight_antipattern: 0.20
  # anti_pattern_phrases:     # filler sentences stripped from generated articles
  #   - "in conclusion"       # omit the key for the bilingual default list; [] disables stripping
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
  hybrid_weight_bm25: 0.7 # lexical channel weight in the fused ranking (all surfaces)
  hybrid_weight_vector: 0.3
  # hybrid_weight_graph: 0.2  # graph-channel fusion weight (default 0.2). NOTE: 0 or
  #                           # negative resolves BACK to the default (an omitted key is
  #                           # also 0) — to turn the channel off, pass channels=bm25,vector
  #                           # per call. Same for hybrid_weight_bm25 / _vector.
  # graph_relation_weights:   # per-relation-type graph weights (built-ins: contradicts 1.1, cites 0.7, others 1.0; 0 excludes a relation from traversal)
  #   my_custom_relation: 1.2
  default_limit: 10
  # query_expansion: true     # LLM query expansion for Q&A (default: true)
  # rerank: true              # LLM re-ranking for Q&A (default: true)
  # rerank_min_coverage: 0.5  # 0.0-1.0 — min fraction of candidates the LLM must score for
  #                           # the rerank blend to apply; below it, pure RRF order is kept.
  #                           # 0 or negative means "use the default", not "no gate" (default: 0.5)
  # chunk_size: 800           # tokens per chunk for indexing (100-5000)
  # pipeline: unified         # search pipeline for MCP/CLI/web/TUI: "unified" (default,
  #                           # chunk+doc fusion, graph channel, recency) or "legacy"
  #                           # (doc-level only) as a RANKING rollback. Any other value is
  #                           # rejected at load. Trust filtering applies on both.
  # chunk_overlap_tokens: 80 # tokens each chunk repeats from its predecessor
  #                          # (default 0 = off; max half of chunk_size).
  #                          # Applies only on `sage-wiki reindex` — change the
  #                          # value and reindex as ONE step (see search-quality.md)
  # graph_expansion: true     # graph-based context expansion for Q&A (default: true)
  # graph_max_expand: 10      # max articles added via graph expansion
  # graph_depth: 2            # ontology traversal depth (1-5)
  # context_max_tokens: 8000  # token budget for query context
  # ann:
  #   enabled: true            # opt-in HNSW approximate search for very large
  #                            # vaults — see search-quality.md (exact stays default)
  # weight_direct_link: 3.0   # graph signal: ontology relation between concepts
  # weight_source_overlap: 4.0 # graph signal: shared source documents
  # weight_common_neighbor: 1.5 # graph signal: Adamic-Adar common neighbors
  # weight_type_affinity: 1.0  # graph signal: entity type pair bonus

# Vector index backend (SPEC-06) — default is the full in-memory matrix
# cache; "mmap" serves an on-disk snapshot with bounded resident memory
# (unix-only ceiling; other platforms fall back to resident with a warn).
# After enabling, run: sage-wiki index rebuild-vectors
# vectors:
#   backend: mmap        # "memory" (default) | "mmap"
#   quantization: none   # "none" (default, fp32 exact) | "int8" (4x smaller,
#                        # measured recall@10 = 0.994 on the reference fixture)

serve:
  transport: stdio # stdio or sse
  port: 3333 # SSE / web UI port

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
#     - name: works_at
#       functional: true           # P3-6: outbound uniqueness — a new edge invalidates the old one
#   entity_types:
#     - name: decision
#       description: "A recorded decision with rationale"
#   triples:                       # LLM triple extraction (P3-2, opt-in)
#     model: ""                    # default: models.extract, then models.summarize
#     max_tokens: 4096
#     max_entities_per_doc: 40
#     max_relations_per_doc: 60
#   temporal:                      # bi-temporal edge validity (P3-6)
#     enabled: true                # DEFAULT — false disables filtering, supersession, as_of
#     auto_apply_threshold: 0.8    # DEFAULT — confidence to auto-invalidate a superseded edge
#   communities:                   # community detection + global queries (P3-5)
#     enabled: true                # default FALSE — costs LLM calls (summaries)
#     model: ""                    # default: models.extract → models.summarize → gpt-4o-mini
#     max_tokens: 1024
#     max_communities: 8           # global-query map breadth
#     min_members: 3               # smaller communities get no summary
#   resolve:                       # entity resolution (P3-3, opt-in)
#     enabled: true                # default false — pairs with triples (see below)
#     model: ""                    # default: models.extract, then models.summarize
#     max_tokens: 4096
#     max_block_size: 60           # candidates per arbitration call
#     auto_apply_threshold: 0.85   # DEFAULT. set exactly 1.0 for review-only (never auto-apply)
#     max_token_df: 0.05           # ignore name tokens shared by >5% of a type
#     min_token_df_floor: 20       # ...but never ignore one seen fewer than 20 times
#     use_embeddings: false        # widen candidates to names sharing no tokens
#     embed_threshold: 0.82
#     max_embed_candidates: 500    # global per-run cap on embedding calls

# Resource limits (SPEC-08) — zero values resolve to safe defaults, never
# "disabled". Every violation returns a typed error + limit_exceeded event.
# See the "limits" section below for the full table.
# limits:
#   max_doc_bytes: 10485760            # 10 MiB
#   max_compile_batch: 1000
#   max_query_bytes: 32768             # 32 KiB
#   provider_timeout: 120s
#   compile_doc_timeout: 15m

# Event stream (SPEC-07) — typed events to a JSONL audit trail; serve mode
# adds webhooks + SSE. See the "events" and "serve.webhooks" sections.
# events:
#   enable: true
#   dir: events

# Remote mirror (SPEC-03) — S3-compatible backup, WAL shipping, hydrate.
# See docs/guides/remote-mirror.md for the full block.
# mirror:
#   enabled: false
```

## Multi-Provider Setup

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


## serve.worker

**Compile worker.** `serve` (MCP and `--ui`) runs a durable compile worker:
sources added while serving are discovered and compiled automatically, with
crash recovery (lease expiry requeues interrupted items) and progress
streaming (`GET /api/compile/status`, `GET /api/compile/progress` SSE).
Tune or disable it:

```yaml
serve:
  worker:
    enabled: true              # default on; false to disable
    poll_interval_seconds: 5
    lease_ttl_seconds: 120
    heartbeat_interval_seconds: 30
    max_attempts: 5            # dead-letter after this many failures
    claim_limit: 16
```

A source that keeps failing is dead-lettered after `max_attempts` failures;
`sage-wiki compile --fresh` (or editing the source) re-queues it.

## events

**Event stream (SPEC-07).** The engine emits a typed event stream for
everything meaningful it does — captures, compile lifecycle, per-doc
outcomes, graph edge changes, entity resolution, searches, mirror passes,
and LLM usage. Delivery is non-blocking: a bounded in-process bus
(drop-oldest under backpressure, drops counted) fans out to the sinks
below. Events never contain document content, raw query text (hashed by
default), or filesystem paths — the workspace is carried as a name only.

```yaml
events:
  enable: true        # default true; master emit switch
  dir: events         # default "events"; JSONL audit trail, workspace-relative
  buffer_size: 1024   # default 1024; bus ring capacity (events)
  stdout: false       # default false; tee events to stdout for piping
  raw_queries: false  # default false; include raw query text in
                      # search_performed (local debug only)
```

With `enable: true`, every compile/capture/search run appends one JSON
object per event to rotating generation files under `events/` (10 MiB per
file, 5 generations kept) — the durable audit trail. Serve mode adds
webhooks and an SSE stream on top of the same bus (see
[serve mode](serve-mode.md) and [webhooks](../webhooks.md)).

## serve.webhooks

**Event delivery to your endpoints (SPEC-07).** Each entry POSTs every
event (or a filtered subset) as JSON, signed with HMAC-SHA256 — see
[webhooks](../webhooks.md) for the signature recipe.

```yaml
serve:
  webhooks:
    - url: https://example.com/hooks/sage-wiki
      secret_env: SAGE_WEBHOOK_SECRET   # OR secret_file: /run/secrets/sage-webhook
      types: [compile_finished]         # omit for all event types
      timeout_seconds: 5                # default 5
      max_retries: 3                    # default 3; 0 = no retries
```

The secret comes from an environment variable or a file — never inline in
config. Failed deliveries retry with exponential backoff (1s/2s/4s) on
5xx, timeouts, and connection errors; 4xx is permanent. Anything still
failing lands in `.sage/webhooks-deadletter.jsonl` (one JSON record per
event, with attempts and last error). Delivery is at-least-once.

## limits

**Resource limits (SPEC-08).** A single block capping every ingestion,
compile, query, and serve surface. Zero (unset) values resolve to the
defaults below — a zero never means "disabled". Every violation fails fast
with a typed `LimitError` and emits a `limit_exceeded` event. Threat model
and residual risks: [Security](../security.md).

| Key | Default | Enforced at |
|-----|---------|-------------|
| `max_doc_bytes` | `10485760` (10 MiB) | one ingested/captured document (stat-before-read; streaming for capture reader) |
| `max_docs_per_capture_batch` | `10` | one MCP `wiki_capture` batch |
| `max_compile_batch` | `1000` | docs entering one compile run (fail-fast before any processing) |
| `max_query_bytes` | `32768` (32 KiB) | search/query/QA question length (web, MCP, serve) |
| `max_graph_traversal_nodes` | `10000` | one Graph-Neighbors BFS (both ontology backends) |
| `max_concurrent_provider_calls` | `20` | concurrent LLM/embed calls in a compile |
| `max_concurrent_requests_per_conn` | `8` | serve per-connection in-flight request guard |
| `provider_timeout` | `120s` | per LLM/embed provider call (shorter caller deadline wins) |
| `compile_doc_timeout` | `15m` | per-document budget over its LLM units (Pass 1 + 2b) |

```yaml
limits:
  max_doc_bytes: 10485760
  max_docs_per_capture_batch: 10
  max_compile_batch: 1000
  max_query_bytes: 32768
  max_graph_traversal_nodes: 10000
  max_concurrent_provider_calls: 20
  max_concurrent_requests_per_conn: 8
  provider_timeout: 120s
  compile_doc_timeout: 15m
```

Serve mode adds fixed HTTP hardening (not part of this block): request
timeouts, a 1 MiB header cap, the per-connection guard, and a 1 MiB `/mcp`
body cap. The `pkg/engine` option `WithLimits` overrides per-caller for
Capture/Search/Graph-Neighbors (compile-path limits read from this block).

## mirror

**Remote mirror (SPEC-03).** S3-compatible backup with WAL shipping and
point-in-time hydrate. The full key set and recipes (SigV4/STS, retain
policy, per-attempt timeouts, generation maps) live in
[Remote mirror](remote-mirror.md); this is the shape of the block.

```yaml
mirror:
  enabled: false              # `mirror enable` sets this
  endpoint: ""                # e.g. https://<acct>.r2.cloudflarestore.com or http://localhost:9000
  addressing: "auto"          # auto = virtual-host for amazonaws.com, path-style otherwise
  bucket: ""
  prefix: ""                  # default: workspace directory name
  region: "auto"              # SigV4 region; "auto" works for R2/MinIO
  access_key_env: "AWS_ACCESS_KEY_ID"    # NAME of env var, never the value
  secret_key_env: "AWS_SECRET_ACCESS_KEY"
  ship_interval: "1s"         # WAL seal cadence while active
  snapshot_interval: "1h"     # scheduled generation cadence
  retain_generations: 2       # PITR depth in rotation count, not time
  encryption:
    enabled: false            # AES-256-GCM client-side encryption
    key_file: ""              # 32-byte key file — MUST live outside the workspace
```

Credentials come from the environment (names configurable) or a
`credentials_file` outside the workspace — never inline in config.
`config.yaml` itself is **not** mirrored (it can hold secrets like
`api.api_key`); hydrate restores data only. See
[Remote mirror](remote-mirror.md) for the full key set, SigV4/STS recipes,
and the retain/rotation semantics.

## Price registry

Cost accounting prices every LLM call through a **registry keyed by
`provider:model`** — never by endpoint kind, so `openai-compatible`,
`qwen`, and `ollama` models are priced by their own entries or not at
all. A model with no registry entry reports `cost: unknown (model not in
price registry)` — never zero, never a guessed default. (Cost numbers
produced before v0.3.0 by `openai-compatible`/`qwen` providers were
mispriced against the OpenAI table and are unreliable.)

Load order (later wins):

1. Embedded defaults (`internal/llm/prices/default.json`) — approximate
   public list prices with per-entry `as_of` dates. **They are estimates;
   verify and override.**
2. User file `~/.sage-wiki/prices.json` — registry shape
   (`{"prices": {"provider:model": {"input": "0.27", "cached_input": "0.07", "output": "1.10", "as_of": "2026-01-01"}}}`,
   decimal strings, empty field = unknown component).
3. Workspace `compiler.price_table` — accepts the registry shape AND the
   legacy PERF-04 shape (`{"provider": {"model": {"input": 0.27, ...}}}`,
   float fields).

A malformed price file is a hard error; a missing file is skipped.
`compiler.token_price_per_million` still beats everything when set.
**Partial entries:** a price entry that sets only some fields leaves the
others *unknown* — the model reports `cost: unknown` rather than charging
zero for the missing components (pre-v0.3.0 partial legacy entries
silently priced missing fields at $0).

Auditing and reporting:

```bash
sage-wiki cost models               # effective registry with as_of + source
sage-wiki cost report               # spend by model and pass/tier
sage-wiki cost report --since 720h  # or RFC3339 / YYYY-MM-DD
```

Every compile, batch, query, and search-expansion call appends a usage
event to `.sage/usage.jsonl` (best-effort: an append failure is logged
and dropped, never fails the call). `cost report` aggregates that ledger;
cached/cache-write token splits are priced at their own rates when the
registry carries them.
