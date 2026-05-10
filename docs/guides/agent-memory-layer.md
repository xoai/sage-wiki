# sage-wiki as a Memory Layer for AI Agents

sage-wiki runs as an MCP server with 17 tools. But tools alone aren't enough — agents won't proactively use the wiki unless their context tells them *when* to check it, *what* to capture, and *how* to query effectively. This guide covers the full setup: connecting MCP, generating skill files, and establishing the read-capture-evolve loop that turns sage-wiki into compounding institutional memory.

## The Problem Skill Files Solve

MCP handles tool discovery and invocation. What it doesn't handle is *when* the agent should voluntarily reach for a tool. A developer installs sage-wiki, adds it to `.mcp.json`, and the agent ignores it because it treats the wiki as one more tool in a pile of tools.

Skill files bridge that gap. They're behavioral instructions — 30-50 lines appended to the agent's instruction file — that create three changes:

1. **When to check the wiki** — without this, agents never look
2. **What to capture** — without this, agents capture nothing or capture everything
3. **How to query effectively** — without this, agents send bad queries and get bad results

## Project Setup for Repo-as-Wiki

The recommended setup keeps sage-wiki as a subdirectory within your project, with sources pointing to your docs and code:

```
my-project/
├── .mcp.json                    # sage-wiki MCP server config
├── CLAUDE.md                    # agent instructions (includes wiki skill)
├── docs/
│   ├── adrs/                    # architecture decision records
│   ├── guides/                  # engineering guides
│   └── architecture.md
├── src/                         # application code
└── .sage-wiki/                  # sage-wiki project root
    ├── config.yaml
    ├── .sage/wiki.db
    ├── .manifest.json
    └── _wiki/                   # compiled output
        ├── concepts/
        ├── summaries/
        └── CHANGELOG.md
```

### Initialize

```bash
cd my-project
mkdir .sage-wiki && cd .sage-wiki
sage-wiki init --skill claude-code
```

This creates the project structure, config.yaml, and appends a skill section to `CLAUDE.md` in one step.

### config.yaml

```yaml
project: my-project
sources:
  - path: ../docs
    type: article
    watch: true
  - path: ../src
    type: code
    watch: true
ignore:
  - node_modules
  - dist
  - .git
  - "*.test.*"
output: _wiki
compiler:
  default_tier: 1              # index + embed everything, compile on demand
  auto_promote: true
  promote_signals:
    query_hit_count: 3
    cluster_size: 5
```

Setting `default_tier: 1` indexes everything fast (FTS5 + vector embedding). Articles compile on demand when an agent queries a topic and `wiki_compile_topic` fires.

### .mcp.json

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", ".sage-wiki"]
    }
  }
}
```

## MCP Setup by Agent

### Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/your/wiki"]
    }
  }
}
```

Generate the skill file:

```bash
sage-wiki init --skill claude-code
# Or for an existing project:
sage-wiki skill refresh --target claude-code
```

This appends behavioral instructions to your CLAUDE.md. The agent will now proactively search the wiki before architectural decisions, capture learnings after significant work, and use ontology queries to explore relationships.

### Cursor

Add to `.cursor/mcp.json` in your project:

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--project", "/path/to/your/wiki"]
    }
  }
}
```

Generate the skill file:

```bash
sage-wiki skill refresh --target cursor
```

This creates `.cursorrules` with behavioral instructions for when to use the wiki. Cursor uses plain text format — markdown headers are automatically converted.

### Windsurf

Same MCP config pattern. Generate the skill file:

```bash
sage-wiki skill refresh --target windsurf
```

Creates `.windsurfrules` in plain text format.

### ChatGPT

ChatGPT supports MCP servers via its remote server protocol. Point it to the SSE endpoint:

```bash
sage-wiki serve --transport sse --port 3333 --project /path/to/your/wiki
```

Then add `http://localhost:3333/sse` as an MCP server in ChatGPT settings.

### Gemini CLI

```bash
sage-wiki skill refresh --target gemini
```

Creates `GEMINI.md` with wiki skill instructions.

### Antigravity / Codex

```bash
sage-wiki skill refresh --target agents-md
# or equivalently:
sage-wiki skill refresh --target codex
```

Both write to `AGENTS.md`.

### Other MCP Clients

Any client that supports MCP can connect via stdio or SSE:

```bash
# stdio (default)
sage-wiki serve --project /path/to/wiki

# SSE (network)
sage-wiki serve --transport sse --port 3333 --project /path/to/wiki
```

For agents without a skill file convention, generate a generic skill and include it manually:

```bash
sage-wiki skill preview --target generic > sage-wiki-skill.md
```

## Domain Skills via Contribution Packs

The base skill file (`sage-wiki init --skill claude-code`) provides generic MCP interaction guidance — when to search, what to capture, how to query. For domain-specific agent behavior, apply a **contribution pack**.

sage-wiki ships 8 bundled packs, each with a `skills/` directory containing domain-specific triggers and capture patterns:

| Pack | Domain | Agent learns to... |
|------|--------|-------------------|
| `academic-research` | Research papers | Search for related work, capture findings, trace citations |
| `software-engineering` | Codebases | Check ADRs before changes, capture decisions, query dependencies |
| `product-management` | PRDs & strategy | Find user stories, capture hypotheses, track validation results |
| `personal-knowledge` | Zettelkasten | Connect ideas, refine fleeting notes, trace inspiration chains |
| `study-group` | Textbook study | Check prerequisites, capture definitions, find examples |
| `meeting-organizer` | Meeting notes | Prep with context, capture decisions, track action items |
| `content-creation` | Editorial | Check for duplication, capture outlines, find references |
| `legal-compliance` | Regulatory | Find applicable regulations, capture obligations, track deadlines |

### Applying a domain pack

```bash
# During init
sage-wiki init --skill claude-code --pack academic-research

# Or on an existing project
sage-wiki pack install academic-research
sage-wiki pack apply academic-research
```

The pack adds domain ontology (entity and relation types), prompt templates, sample sources, and a skill file. The skill file is copied to your project's `skills/` directory with domain-specific triggers like "search for related papers before citing a claim" or "capture decisions made during meetings."

### Two-layer skill architecture

The skill system has two layers:

1. **Base skill** (generated by `--skill`) — generic MCP instructions rendered from your project's config.yaml. Written to CLAUDE.md/.cursorrules/etc. Covers tool names, entity types, relation types.

2. **Domain skill** (from pack) — domain-specific triggers, capture patterns, and query examples. Copied to `skills/` during `pack apply`. Complements the base layer with contextual guidance.

Both layers work together. The base skill teaches the agent *how* to use wiki tools. The domain skill teaches it *when* and *why* — in the language of your domain.

## What the Generator Produces

The base skill content is generated from your project's config.yaml, not hand-written. The generator reads:

- **Project name** — referenced in the skill header
- **Source types** — listed for context
- **Entity types** — built-in (concept, technique, source, claim, artifact) + custom types from `ontology.entity_types` + any types added by packs
- **Relation types** — built-in (implements, extends, contradicts, etc.) + custom from `ontology.relation_types` + pack relations
- **Graph expansion** — whether ontology queries are available (affects query examples)

The output is ~30 lines of behavioral instructions with three sections: when to check, what to capture, how to query. It references MCP tool names but not full schemas (agents get those from MCP discovery). Domain-specific guidance comes from pack skill files in `skills/`.

### Marker-based updates

The generated skill section is wrapped in markers:

```
<!-- sage-wiki:skill:start -->
...generated content...
<!-- sage-wiki:skill:end -->
```

For plain-text files (.cursorrules, .windsurfrules):

```
# sage-wiki:skill:start
...
# sage-wiki:skill:end
```

Running `sage-wiki skill refresh` replaces only the content between markers. Your other instructions are preserved.

## Capture Workflows

sage-wiki provides several MCP tools for different capture patterns:

### Quick Capture: `wiki_capture`

The primary tool for saving knowledge from conversations. Give it a chunk of text and it uses the LLM to extract the key learnings automatically.

**Example prompts you can say to your AI:**

> "Save what we just figured out about connection pooling to my wiki"

> "Capture the key decisions from this conversation"

> "Extract the important findings from our debugging session and add them to my wiki"

The AI will call `wiki_capture` with the relevant text. The tool:
1. Sends the text to your configured LLM for knowledge extraction
2. Writes each extracted item as a source file in `raw/captures/`
3. Returns a summary of what was captured

Run `sage-wiki compile` afterward to process captures into wiki articles with concepts, cross-references, and search indexing.

### Single Nugget: `wiki_learn`

For storing a specific learning or insight without LLM extraction:

> "Remember that SQLite FTS5 requires double-quoting for phrase search"

The AI calls `wiki_learn` with a type (gotcha, correction, convention, error-fix, api-drift) and the content. These are stored in the learning database and surfaced during linting.

### Full Document: `wiki_add_source`

For adding an existing file as a wiki source:

> "Add the file at raw/papers/attention.pdf to my wiki sources"

### Compile: `wiki_compile`

After capturing knowledge, compile to process everything:

> "Compile my wiki to process the new captures"

## The Read-Capture-Evolve Loop

The skill file's purpose is to create a virtuous cycle:

```
Session starts
  → Agent reads CLAUDE.md (includes wiki skill)
  → Agent checks wiki_status (knows what's available)
  │
  ├─ Agent gets a task
  │  → Checks wiki before acting (behavioral trigger)
  │  → Finds relevant context (or finds nothing — signals gap)
  │  → Completes the task with wiki context
  │  → Captures decision/gotcha/convention (capture trigger)
  │
  ├─ Next session: wiki is smarter
  │  → New capture is indexed (Tier 1) or compiled (Tier 3)
  │  → Agent finds more context next time
  │  → Better decisions, fewer repeated mistakes
  │
  └─ Over time: wiki compounds
     → Architectural decisions accumulate
     → Convention knowledge densifies
     → New team members get context from day 1
     → The wiki is the institutional memory
```

The difference between "sage-wiki is installed" and "sage-wiki is actively used" is entirely in whether this loop runs. The skill file is the bootstrap that starts it.

## Adding Domain Skills to Your Own Pack

Domain skills are plain markdown files in the pack's `skills/` directory. Unlike the base skill template (which is a Go template with project-specific variables), pack skills are static — they use domain-specific language and reference the pack's ontology relations directly.

### Create a pack with a skill file

```bash
sage-wiki pack create my-domain
```

Then add `skills/my-domain.md` to your pack directory:

```markdown
## My Domain — domain triggers

### When to search the wiki

- Before [domain-specific trigger] → search for [what]
- "What's known about X?" → search for [domain concept]

### Domain-specific queries

- Find [domain concept]: `wiki_search("query")`
- Check [relationship]: `wiki_ontology_query` with relation "[your_relation]"

### What to capture

- [Domain artifact] → `wiki_learn` type "[type]" with [what to include]
- [Rich context] → `wiki_capture` from [source]
```

List the skill file in your `pack.yaml`:

```yaml
skills:
  - my-domain.md
```

When users run `sage-wiki pack apply my-domain`, the skill file is copied to `skills/my-domain.md` in their project. Keep skills under 40 lines — focused triggers are more effective than comprehensive documentation.

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for the full pack authoring guide and schema reference.

## Tips for Effective Capture

1. **Be specific about what to save.** "Save the part about retry backoff" is better than "save everything."

2. **Add context.** "We were debugging the auth middleware" helps the extraction focus on what matters.

3. **Tag your captures.** Tags help with search and organization later: "tag this with go, performance."

4. **Compile regularly.** Captured items sit in `raw/captures/` until compiled. The compiler extracts concepts, discovers connections to existing articles, and builds the wiki graph.

5. **Review captures.** Check `raw/captures/` occasionally. The LLM extraction is good but not perfect — you may want to edit or merge items.

## What Gets Captured

The extraction prompt focuses on:

- **Decisions** made during the conversation
- **Discoveries** or "aha moments"
- **Corrections** where an assumption was wrong
- **Technical facts** that were established
- **Patterns and anti-patterns** identified

It skips greetings, retries, debugging dead-ends, and other noise.

## Example

Say you're debugging a performance issue with your AI and discover that the bottleneck is in the database connection pool, not the query itself. At the end of the session:

> "Capture the key findings from this debugging session. Tag with postgres, performance."

The AI extracts items like:
- "connection-pool-bottleneck" — The actual performance issue was exhausted connections, not slow queries
- "pgbouncer-transaction-mode" — Transaction-level pooling resolved the issue; session-level was causing connection hoarding

These become source files that the compiler weaves into your wiki's knowledge graph. Next time an agent encounters a database performance question, `wiki_search("connection pooling")` surfaces these findings — the wiki remembers what you learned.
