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
  hybrid_weight_bm25: 0.7 # BM25 vs vector weight
  hybrid_weight_vector: 0.3
  # hybrid_weight_graph: 0.2  # graph-channel fusion weight (0.2 default; ontology proximity joins ranking)
  # graph_relation_weights:   # per-relation-type graph weights (built-ins: contradicts 1.1, cites 0.7, others 1.0; 0 excludes a relation from traversal)
  #   my_custom_relation: 1.2
  default_limit: 10
  # query_expansion: true     # LLM query expansion for Q&A (default: true)
  # rerank: true              # LLM re-ranking for Q&A (default: true)
  # rerank_min_coverage: 0.5  # min fraction of candidates the LLM must score for the rerank blend to apply; below it, RRF order is kept (default: 0.5)
  # chunk_size: 800           # tokens per chunk for indexing (100-5000)
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
#   entity_types:
#     - name: decision
#       description: "A recorded decision with rationale"
#   triples:                       # LLM triple extraction (P3-2, opt-in)
#     enabled: true                # default false — adds 1 LLM call per Tier-3 doc
#     model: ""                    # default: models.extract, then models.summarize
#     max_tokens: 4096
#     max_entities_per_doc: 40
#     max_relations_per_doc: 60
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

## Price table override

Cost estimates use built-in per-model prices
(which may go stale). Point `compiler.price_table` at a JSON file (same
shape as the built-in map) to override them per provider/model; built-ins
remain the fallback.
