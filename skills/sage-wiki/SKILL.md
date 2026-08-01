---
name: sage-wiki
description: Reference skill for sage-wiki — local-first knowledge graph with MCP server, REST API, compiled wiki, and Obsidian-compatible output.
version: "0.1.0"
generated: skillgen — do not hand-edit
---

# sage-wiki

Local-first knowledge graph that compiles documents into an interlinked, human-readable wiki with provenance. Exposes 19 MCP tools over stdio/SSE and a versioned REST API under `/v1/`. Output is Obsidian-compatible markdown.

## How to connect

**MCP (primary):** The server runs as a subprocess over stdio, or over SSE/HTTP:
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

**REST (`/v1/`):** When the server is running with `--ui`, the REST facade is at `/v1/`. Auth is Bearer-token (same as `SAGE_WIKI_TOKEN`); loopback is zero-config. All errors use a fixed `{error: {code, message, details}}` envelope.

## The 19 MCP Tools


### `wiki_add_ontology` (write)
Create an ontology entity or relation.
- **REST:** POST /v1/ontology/entities / POST /v1/ontology/relations
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `entity_id` | string | no | — |
| `entity_name` | string | no | — |
| `entity_type` | string | no | — |
| `relation` | string | no | — |
| `source_id` | string | no | — |
| `target_id` | string | no | — |



### `wiki_add_source` (write)
Add a source file to a source folder and update the manifest.
- **REST:** POST /v1/sources
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `path` | string | yes |  |
| `type` | string | no | — |



### `wiki_capture` (write)
Capture knowledge from a conversation or text.
- **REST:** POST /v1/capture
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `content` | string | yes |  |
| `context` | string | no | — |
| `tags` | string | no | — |



### `wiki_commit` (write)
Git add and commit all changes.
- **REST:** POST /v1/git/commit
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `message` | string | no | — |



### `wiki_compile` (async)
Run the full compile pipeline: diff → summarize → extract concepts → write articles.
- **REST:** POST /v1/jobs/compile (async — 202 + job_id)
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `dry_run` | boolean | no | false |
| `fresh` | boolean | no | false |
| `prune` | boolean | no | false |



### `wiki_compile_diff` (read)
Show added/modified/removed source files compared to the manifest.
- **REST:** GET /v1/compile/diff


### `wiki_compile_topic` (async)
Compile sources for a specific topic on demand.
- **REST:** POST /v1/jobs/compile?topic=... (async — 202 + job_id)
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `max_sources` | number | no | — |
| `topic` | string | yes |  |



### `wiki_graph_query` (read)
Answer a relational question by graph traversal: seed entities are resolved from the question (aliases resolve to their canonical entity), a bounded multi-hop subgraph is serialized, and the answer is grounded ONLY in those edges — every citation carries source_doc and confidence provenance.
- **REST:** POST /v1/graph/query
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `as_of` | string | no | — |
| `hops` | number | no | — |
| `max_edges` | number | no | — |
| `mode` | string | no | — |
| `question` | string | yes |  |



### `wiki_learn` (write)
Store a learning entry for the self-learning loop.
- **REST:** POST /v1/learnings
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `content` | string | yes |  |
| `tags` | string | no | — |
| `type` | string | yes |  |



### `wiki_lint` (async)
Run linting passes on the wiki.
- **REST:** POST /v1/jobs/lint (async — 202 + job_id)
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `fix` | boolean | no | false |
| `pass` | string | no | — |



### `wiki_list` (read)
List wiki articles, optionally filtered by entity type.
- **REST:** GET /v1/entities
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `type` | string | no | — |



### `wiki_ontology_query` (read)
Query the ontology graph.
- **REST:** GET /v1/ontology/{entity}/traverse
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `depth` | number | no | — |
| `direction` | string | no | — |
| `entity` | string | yes |  |
| `relation` | string | no | — |



### `wiki_provenance` (read)
Show source-article provenance.
- **REST:** GET /v1/provenance
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `article` | string | no | — |
| `source` | string | no | — |



### `wiki_query` (compound)
Ask a free-form question against the wiki: searches sources and compiled articles, synthesizes a cited answer with the LLM (spends LLM budget), and files the result to wiki/under_review/ by default (trust output review) or wiki/outputs/ only when trust include_outputs is 'true'.
- **REST:** —
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `question` | string | yes |  |
| `top_k` | number | no | — |



### `wiki_read` (read)
Read the full content of a wiki article by path.
- **REST:** GET /v1/articles/{path}
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `path` | string | yes |  |



### `wiki_search` (read)
Search the wiki with hybrid retrieval: BM25 + vector over documents and chunks, fused with ontology-graph proximity.
- **REST:** GET /v1/search
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `boost_tags` | string | no | — |
| `channels` | string | no | — |
| `expand` | boolean | no | false |
| `limit` | number | no | — |
| `query` | string | yes |  |
| `rerank` | boolean | no | false |
| `tags` | string | no | — |



### `wiki_status` (read)
Show wiki stats: sources, concepts, entries, vectors, entities, relations.
- **REST:** GET /v1/status


### `wiki_write_article` (write)
Write a concept article, create ontology entity, and embed vector.
- **REST:** PUT /v1/articles/{concept}
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `concept` | string | yes |  |
| `content` | string | yes |  |



### `wiki_write_summary` (write)
Write a summary markdown file, index in FTS5, and optionally embed vector.
- **REST:** PUT /v1/summaries
| Argument | Type | Required | Default |
|----------|------|----------|---------|
| `concepts` | string | no | — |
| `content` | string | yes |  |
| `source` | string | yes |  |




## Error Codes

Branch on `code`, never on `message`:

| Code | HTTP | When |
|------|------|------|
| `invalid_argument` | 400 | Missing, malformed, or out-of-range argument. |
| `unauthenticated` | 401 | Missing or invalid Bearer token. |
| `forbidden` | 403 | Host not allowed; path containment violation. |
| `not_found` | 404 | Article, entity, or job does not exist. |
| `conflict` | 409 | Compile already in progress; job already finished. |
| `feature_disabled` | 412 | `as_of` without temporal enabled; `mode=global` without communities enabled. |
| `payload_too_large` | 413 | Capture content over 100 KB. |
| `internal` | 500 | Unclassified tool failure. Message must not leak paths. |
| `unavailable` | 503 | Backend / store unavailable. |


## Opt-In Features

All opt-in flags live under `config.yaml` → `ontology`:

| Flag | Default | Unlocks |
|------|---------|---------|
| `ontology.temporal.enabled` | true | Historical queries (`as_of` in graph_query), temporal validity edges. |
| `ontology.triples.enabled` | false | Subject-predicate-object fact extraction from articles. |
| `ontology.resolve.enabled` | false | Entity resolution — merges duplicate entities across documents. |
| `ontology.communities.enabled` | false | Community detection via Louvain; `mode=global` in graph_query. |


## Tiers

Sources are assigned a tier determining compile depth:

| Tier | Label | What Happens |
|------|-------|--------------|
| 0 | Index | File metadata only — no LLM cost. |
| 1 | Embed | Vector embeddings — no LLM summarization. |
| 3 | Full Compile | Summarize → extract concepts → write articles. Tier 3 is ~5–8 min/doc. There is no Tier 2. |


**There is no Tier 2.** Tiers go 0 → 1 → 3.

## Important Correctness Points

1. Tiers are **0 / 1 / 3** — there is no Tier 2. Documents and tooltips that claim a Tier 2 are wrong.
2. `ontology.temporal.enabled` defaults **true** (per code). `ontology.triples`, `ontology.resolve`, and `ontology.communities` default **false**.
3. `as_of` requires `ontology.temporal.enabled`; `mode=global` requires `ontology.communities.enabled`.
4. `compile` and `lint` are **job submissions** — they return `202 Accepted` with a `job_id`, not a blocking result. Poll `GET /v1/jobs/{id}` for status.
5. Evidence spans (`provenance`) quote the **compiled summary**, not the source document. The summary is the distilled, LLM-processed form.

## Install

```bash
# MCP (stdio — primary integration path)
sage-wiki serve --project /path/to/wiki

# REST (with Web UI)
sage-wiki serve --ui --project /path/to/wiki

# Agent skill installation
npx skills add https://github.com/xoai/sage-wiki --skill sage-wiki
```

> **Pre-1.0 notice:** Every surface here is experimental. Tool semantics, argument names, and REST routes may change between versions. Pin the version you depend on.

Generated by tools/skillgen — 19 tools in registry.
