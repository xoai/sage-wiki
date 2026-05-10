# sage-wiki for Teams

sage-wiki is designed for solo use by default, but the same features
scale to teams of 3-50 people working from a shared knowledge base.
This guide covers the setup patterns, sync strategies, and workflows
that make sage-wiki an institutional memory for your team.

## What Teams Get

A shared sage-wiki gives every team member and every AI agent the same
view of institutional knowledge. New hires search the wiki instead of
asking around. Agents check the wiki before asking the user. Meeting
decisions are captured, compiled, and cross-referenced automatically.

Concrete outcomes:

- **Onboarding in hours, not weeks.** New engineers search the wiki for
  architecture decisions, API conventions, and domain context. The wiki
  answers questions about why things are built the way they are.
- **Agent context that compounds.** Every AI agent on the team reads
  from and writes to the same knowledge base. What one agent learns,
  all agents know.
- **Decisions don't get lost.** Meeting notes, Slack conversations, and
  design discussions are captured and compiled into searchable articles.
- **Wrong answers don't spread.** The trust system quarantines LLM
  outputs until they're grounding-verified and consensus-confirmed.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                    Team Members                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐              │
│  │ Dev + AI  │  │ Dev + AI  │  │ PM + AI   │             │
│  │  Agent    │  │  Agent    │  │  Agent    │             │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘              │
│       │MCP          │MCP          │MCP                   │
└───────┼─────────────┼─────────────┼─────────────────────┘
        │             │             │
        v             v             v
┌──────────────────────────────────────────────────────────┐
│                sage-wiki MCP Server                       │
│  18 read tools + 8 write tools + 4 compound tools        │
│  wiki_search · wiki_capture · wiki_compile_topic         │
│  wiki_learn · wiki_query · wiki_add_source               │
└───────────────────────┬──────────────────────────────────┘
                        │
         ┌──────────────┼──────────────┐
         v              v              v
┌──────────────┐ ┌────────────┐ ┌────────────────┐
│  Compiler    │ │ Trust      │ │  Search        │
│  Pipeline    │ │ Pipeline   │ │  Pipeline      │
│ (tiered,     │ │ (consensus │ │ (chunk-level,  │
│  parallel)   │ │  quorum)   │ │  graph-expand) │
└──────┬───────┘ └─────┬──────┘ └────────┬───────┘
       │               │                 │
       v               v                 v
┌──────────────────────────────────────────────────────────┐
│                   .sage/wiki.db                           │
│  FTS5 · Vectors · Ontology · Chunks · Trust · Learnings  │
└──────────────────────────────────────────────────────────┘
         │
         v
┌──────────────────────────────────────────────────────────┐
│                   wiki/ (output)                          │
│  concepts/ · summaries/ · outputs/ · under_review/       │
│  Git-tracked → push to shared repo                       │
└──────────────────────────────────────────────────────────┘
```

## Setup Patterns

There are three ways to run sage-wiki for a team. Pick the one that
matches your infrastructure.

### Pattern A: Git-Synced Shared Wiki

The simplest setup. The wiki lives in a Git repo. Everyone clones it,
compiles locally, and pushes changes. Best for teams of 3-10 with
shared Git access.

```
team-wiki/
├── config.yaml
├── raw/                    # shared source documents
│   ├── architecture/
│   ├── meeting-notes/
│   ├── onboarding/
│   └── runbooks/
├── wiki/                   # compiled output (git-tracked)
│   ├── concepts/
│   ├── summaries/
│   └── outputs/
├── .sage/wiki.db           # .gitignore this — rebuilt on compile
├── .manifest.json
└── .mcp.json               # agent MCP config
```

**Setup:**

```bash
mkdir team-wiki && cd team-wiki
sage-wiki init
git init && git remote add origin git@github.com:your-org/team-wiki.git

# Configure
cat >> config.yaml << 'EOF'
trust:
  include_outputs: verified
  consensus_threshold: 3
compiler:
  auto_commit: true
  auto_lint: true
EOF

# Add initial sources
cp -r ~/onboarding-docs raw/onboarding/
cp -r ~/architecture-docs raw/architecture/

# First compile
sage-wiki compile

# Push
git add -A && git commit -m "initial wiki" && git push
```

**Daily workflow:**

```bash
git pull                             # get team changes
sage-wiki compile                    # rebuild index + compile new sources
# ... work, capture knowledge ...
git add -A && git commit -m "update" && git push
```

**What to .gitignore:**

```gitignore
.sage/wiki.db
.sage/*.db-*
```

The database is rebuilt from the compiled wiki on each `sage-wiki compile`.
The wiki/ directory and .manifest.json are tracked — they contain the
compiled output that teammates read.

### Pattern B: Shared Server with Web UI

Run sage-wiki on a server. Everyone accesses the wiki through the
browser and their agents connect via MCP over SSE. Best for teams of
5-30 who want a central knowledge portal.

**Docker Compose:**

```yaml
# docker-compose.yml
services:
  sage-wiki:
    image: ghcr.io/xoai/sage-wiki:latest
    ports:
      - "3333:3333"
    volumes:
      - ./wiki-data:/wiki
    environment:
      - GEMINI_API_KEY=${GEMINI_API_KEY}
    restart: unless-stopped
```

```bash
docker compose up -d
# Open http://your-server:3333
```

**Agent connection via SSE:**

Each developer adds the server to their `.mcp.json`:

```json
{
  "mcpServers": {
    "sage-wiki": {
      "url": "http://wiki.internal:3333/sse"
    }
  }
}
```

Generate a skill file so agents know when to use the wiki:

```bash
sage-wiki skill refresh --target claude-code
```

See the [self-hosted guide](self-hosted-server.md) for reverse proxy,
TLS, and Syncthing sync setup.

### Pattern C: Hub Federation (Multi-Project)

Large teams often have multiple knowledge bases — one per project or
domain. The hub system federates them into a single search interface.

```bash
# Register projects
sage-wiki hub add /projects/backend-wiki --description "Backend services"
sage-wiki hub add /projects/ml-wiki --description "ML models and data"
sage-wiki hub add /projects/ops-wiki --description "Infrastructure"

# Search across all projects
sage-wiki hub search "deployment process"

# Check status of all projects
sage-wiki hub status

# Compile all projects
sage-wiki hub compile --all
```

Each project maintains its own config, sources, and compilation
pipeline. Hub search queries all projects marked as `searchable: true`.

**Use cases:**
- Engineering + product + design each have their own wiki, but can
  cross-search
- Per-service wikis that share common infrastructure knowledge
- Research wiki separate from codebase wiki, both searchable

## Configuring for Teams

### Recommended config.yaml

```yaml
version: 1
project: team-wiki
description: "Shared knowledge base for the engineering team"

sources:
  - path: raw
    type: auto
    watch: true

output: wiki

api:
  provider: gemini                  # or openai, anthropic
  api_key: ${GEMINI_API_KEY}        # env var — never hardcode
  rate_limit: 60

models:
  summarize: gemini-2.0-flash       # cheap for bulk
  extract: gemini-2.0-flash
  write: gemini-2.5-pro             # quality for articles
  lint: gemini-2.0-flash
  query: gemini-2.5-pro

compiler:
  max_parallel: 20
  auto_commit: true
  auto_lint: true
  default_tier: 1                   # index everything, compile on demand
  auto_promote: true

search:
  query_expansion: true
  rerank: true
  graph_expansion: true

trust:
  include_outputs: verified
  consensus_threshold: 3
  grounding_threshold: 0.8
  auto_promote: true
```

**Key choices for teams:**

- **`default_tier: 1`** — Index all sources fast (embedding only),
  compile on demand when queried. A 10K-source wiki indexes in hours
  instead of days.
- **`trust.include_outputs: verified`** — Quarantine LLM answers until
  verified. Prevents one person's wrong query from poisoning everyone's
  context.
- **`auto_commit: true`** — Every compile creates a git commit. Full
  audit trail of what changed and when.
- **Env var for API key** — Never commit secrets. Use `${VAR}` syntax.

### Subscription Auth for Teams

If your team has individual LLM subscriptions (ChatGPT Pro, Claude Max),
each member can use their own subscription:

```bash
# Each team member runs once:
sage-wiki auth login --provider openai
# or
sage-wiki auth import --provider claude
```

```yaml
# config.yaml — shared
api:
  provider: openai
  auth: subscription
```

Credentials are stored per-user at `~/.sage-wiki/auth.json` (0600
permissions). The shared config just says "use subscription" — each
person's local credentials are used. No API keys in the repo.

## Source Organization for Teams

A well-organized source directory is the foundation. Here's a structure
that works for engineering teams:

```
raw/
├── architecture/
│   ├── system-overview.md          # high-level architecture
│   ├── data-model.md               # database schema docs
│   └── adrs/                       # architecture decision records
│       ├── 001-use-postgres.md
│       ├── 002-event-sourcing.md
│       └── 003-grpc-over-rest.md
├── onboarding/
│   ├── getting-started.md
│   ├── local-dev-setup.md
│   └── team-conventions.md
├── runbooks/
│   ├── deploy-production.md
│   ├── incident-response.md
│   └── database-migration.md
├── meeting-notes/
│   ├── 2026-05-01-sprint-planning.md
│   └── 2026-05-08-architecture-review.md
├── research/
│   ├── papers/                     # PDFs, EPUBs
│   └── evaluations/                # tool/library evaluations
├── code/                           # symlink or copy of key code
│   ├── api-handlers.go
│   └── domain-models.py
└── captures/                       # auto-created by wiki_capture
    └── capture-2026-05-09-*.md
```

**Tips:**

- **ADRs are high-value sources.** They contain *why* decisions were
  made, which is exactly what agents and new hires need.
- **Meeting notes compound.** A single meeting note is noise. Compiled
  across 50 meetings, sage-wiki extracts recurring decisions, action
  items, and architectural themes.
- **Code as source.** Link key files (API handlers, domain models) so
  the wiki understands your codebase structure. sage-wiki has 10 code
  parsers (Go, Python, TypeScript, Rust, Java, C/C++, Ruby, JSON, YAML).
- **Captures are automatic.** When agents use `wiki_capture`, extracted
  knowledge lands in `raw/captures/` and gets compiled on the next run.

## Agent Integration

The real power for teams comes when every developer's AI agent reads
from and writes to the shared wiki.

### Step 1: Connect MCP

Add sage-wiki to each developer's MCP config:

**Claude Code (`.mcp.json`):**

```json
{
  "mcpServers": {
    "sage-wiki": {
      "command": "sage-wiki",
      "args": ["serve", "--transport", "stdio"],
      "cwd": "/path/to/team-wiki"
    }
  }
}
```

**Cursor / Windsurf / Codex:** Same pattern with the appropriate
config file location.

### Step 2: Generate Skill File

```bash
cd /path/to/team-wiki
sage-wiki skill refresh --target claude-code
```

This appends behavioral instructions to your agent's instruction file
(CLAUDE.md, .cursorrules, etc.) that teach the agent:

- **When to search** — before answering architecture questions, before
  making design decisions, when encountering unfamiliar code
- **What to capture** — decisions, corrections, conventions, gotchas
- **How to query** — use specific terms, check ontology for related
  concepts, follow `[[wikilinks]]`

### Step 3: The Read-Capture-Evolve Loop

Once connected, agents naturally fall into a virtuous cycle:

1. **Read** — Agent searches the wiki before answering. Gets context
   about team conventions, past decisions, known gotchas.
2. **Capture** — Agent learns something new during a session (a
   correction, a convention, a gotcha). Captures it via `wiki_capture`
   or `wiki_learn`.
3. **Compile** — Next `sage-wiki compile` processes the captured
   knowledge into articles and cross-references.
4. **Evolve** — The wiki grows. Future queries return better context.
   The team's collective knowledge compounds.

**Example: Agent learns a convention**

```
Developer: "No, we always use UTC timestamps in the API, never local time."
Agent: [calls wiki_learn(type: "convention", content: "All API timestamps
        must be UTC. Local time is never accepted or returned.")]
```

Next time any agent on the team deals with timestamps, the wiki search
returns this convention.

### Step 4: On-Demand Compilation

Agents can trigger compilation for specific topics without waiting for
a full compile run:

```
Agent: [calls wiki_compile_topic("authentication flow")]
```

This finds uncompiled sources related to authentication, promotes them
to Tier 3, and compiles just that cluster. Takes ~2 minutes for 20
sources instead of hours for the full corpus.

## Knowledge Capture Workflows

### Meeting Notes Pipeline

After each meeting, drop the notes (or transcript) into `raw/meeting-notes/`:

```bash
# From a transcript file
sage-wiki capture --file meeting-transcript.vtt --tags "sprint-planning"

# Or pipe from clipboard
pbpaste | sage-wiki capture --file - --tags "architecture-review"
```

Or let agents capture directly during the meeting:

```
Agent: [calls wiki_capture(content: "<paste of meeting notes>",
        context: "Architecture review meeting",
        tags: ["architecture", "q2-planning"])]
```

The capture command extracts knowledge items and saves them as source
files. Next `sage-wiki compile` processes them into searchable articles.

### Session Scribe (Automatic)

sage-wiki can extract entities from AI agent session transcripts:

```bash
sage-wiki scribe ~/.claude/sessions/latest.jsonl
```

This parses the session, identifies entities (decisions, conventions,
gotchas), compares against existing ontology, and adds new ones. Up to
10 entities per session to prevent noise.

### Learning Types

The `wiki_learn` MCP tool and `sage-wiki learn` CLI accept typed
learnings that compile differently:

| Type | When | Example |
|------|------|---------|
| `convention` | Team agrees on a pattern | "API responses always wrap in `{data, error}`" |
| `gotcha` | Non-obvious trap | "Redis SCAN can return duplicates across pages" |
| `correction` | Someone was wrong | "Auth tokens expire in 1h, not 24h" |
| `error-fix` | A bug and its fix | "OOM on large PDFs — set GOMAXPROCS=4" |
| `api-drift` | API changed | "Stripe v2025-04 removed `charges.list`" |

### Bulk Import

For bootstrapping a team wiki from existing documentation:

```bash
# Import Confluence exports
cp -r ~/confluence-export/*.md raw/confluence/

# Import from a docs repo
ln -s ~/docs-repo/guides raw/engineering-guides

# Import Notion exports
cp -r ~/notion-export raw/notion/

# Compile everything
sage-wiki compile
```

sage-wiki handles Markdown, PDF, Word (.docx), Excel (.xlsx),
PowerPoint (.pptx), EPUB, email (.eml), CSV, images, and code files.

## Trust System for Teams

The trust system is especially important for teams because multiple
agents produce outputs that enter the shared corpus. Without trust
gates, one agent's hallucination becomes every agent's context.

### How It Protects Teams

1. **No self-reinforcing errors.** Query outputs go to `under_review/`,
   not directly into search. Wrong answers can't pollute future queries.

2. **Consensus from multiple sessions.** When different developers
   (or different agents) ask the same question and get the same answer
   from independent source chunks, confidence increases.

3. **Source change invalidation.** When someone updates a source
   document, any outputs that cited it are automatically demoted. They
   must be re-verified against the updated source.

4. **Conflict detection.** When the same question produces contradictory
   answers (maybe from different source subsets), both are flagged for
   human review.

### Recommended Trust Config for Teams

```yaml
trust:
  include_outputs: verified
  consensus_threshold: 3       # require 3 independent confirmations
  grounding_threshold: 0.8     # 80% of claims must be source-grounded
  auto_promote: true
```

### Periodic Verification

Add a cron job or CI step to verify pending outputs:

```bash
# Weekly verification of recent outputs
sage-wiki verify --since 7d --limit 50

# Monthly cleanup of stale outputs
sage-wiki outputs clean --older-than 90d
```

## Scaling Considerations

### Tiered Compilation for Large Teams

Teams with thousands of source documents should use tiered compilation:

```yaml
compiler:
  default_tier: 1              # index + embed everything
  auto_promote: true           # compile on demand
  tier_defaults:
    json: 0                    # structured data — index only
    yaml: 0
    lock: 0
    log: 0
```

This indexes everything in the first pass (hours, not days), then
compiles individual topic clusters on demand when agents query them.

### Cost Management

For teams, LLM costs are shared. Track usage per compile:

```bash
sage-wiki compile --estimate    # preview cost before compiling
```

**Cost-saving strategies:**

- Use cheap models for summarization (`gemini-2.0-flash`, `claude-haiku`)
  and quality models only for article writing and Q&A
- Enable prompt caching (`compiler.prompt_cache: true`) for 50-90%
  input token savings
- Use batch mode for initial compilation (`sage-wiki compile --batch`)
  for 50% cost reduction
- Set `default_tier: 1` to avoid compiling low-value sources

### Performance at Scale

| Wiki Size | Index Time | Compile Time | DB Size | Search Latency |
|-----------|-----------|--------------|---------|---------------|
| 100 docs | 2 min | 15 min | 5 MB | <50ms |
| 1K docs | 20 min | 2 hours | 50 MB | <100ms |
| 10K docs | 3 hours | On demand | 500 MB | <200ms |
| 100K docs | 5.5 hours | On demand | 5 GB | <500ms |

Index time is for Tier 1 (embed only). Compile time is for full Tier 3
on the entire corpus — with tiered compilation and on-demand mode,
actual compile time is much lower because only queried topics are
compiled.

## Recipes

### Recipe: Engineering Wiki for a Startup (5-15 people)

```bash
mkdir eng-wiki && cd eng-wiki
sage-wiki init --skill claude-code

# Sources: ADRs, guides, runbooks, API docs
mkdir -p raw/{adrs,guides,runbooks,api-docs}

# Config
cat > config.yaml << 'YAML'
version: 1
project: eng-wiki
description: "Engineering knowledge base"
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}
models:
  summarize: gemini-2.0-flash
  extract: gemini-2.0-flash
  write: gemini-2.5-pro
  lint: gemini-2.0-flash
  query: gemini-2.5-pro
compiler:
  auto_commit: true
  auto_lint: true
trust:
  include_outputs: verified
YAML

sage-wiki compile
git init && git add -A && git commit -m "init eng-wiki"
```

### Recipe: Research Lab Wiki (Papers + Code)

```bash
mkdir lab-wiki && cd lab-wiki
sage-wiki init --skill claude-code --pack research-library

mkdir -p raw/{papers,code,notes}
cp ~/papers/*.pdf raw/papers/

cat >> config.yaml << 'YAML'
compiler:
  default_tier: 1               # embed papers fast
  auto_promote: true            # compile when queried
search:
  graph_expansion: true         # discover paper connections
  graph_depth: 3                # deeper traversal for citation chains
ontology:
  relation_types:
    - name: cites
      synonyms: ["cites", "引用", "references"]
    - name: extends
      synonyms: ["extends", "builds on", "improves upon"]
YAML

sage-wiki compile
```

### Recipe: Obsidian Vault Overlay for a Team

```bash
cd ~/TeamVault
sage-wiki init --vault --skill cursor

cat >> config.yaml << 'YAML'
ignore:
  - Daily Notes
  - Personal
  - Templates
trust:
  include_outputs: verified
compiler:
  auto_commit: true
YAML

sage-wiki compile
sage-wiki compile --watch        # live recompilation
```

Each team member opens the same vault in Obsidian and sees compiled
articles alongside their own notes. The `ignore` list keeps personal
folders private.

## Troubleshooting

### "My agent doesn't use the wiki"

1. Check MCP connection: `sage-wiki serve --transport stdio` should
   start without errors
2. Verify skill file exists: check for wiki-related instructions in
   CLAUDE.md / .cursorrules
3. Regenerate skill file: `sage-wiki skill refresh --target <agent>`
4. Check wiki has content: `sage-wiki status` should show indexed entries

### "Compiles are too slow"

- Set `default_tier: 1` to skip full compilation for most sources
- Use `sage-wiki compile --batch` for initial large compilations
- Increase `max_parallel` (default 20) if your provider allows it
- Use cheaper models for summarization

### "Search returns poor results"

- Run `sage-wiki compile` to build chunk index (needed for enhanced search)
- Check `sage-wiki status` for vector count — 0 vectors means no
  embeddings
- Enable graph expansion in config for better context discovery
- For multilingual teams, ensure your embedding model supports your
  languages

### "Database conflicts when multiple people compile"

The `.sage/wiki.db` SQLite database is not safe for concurrent writes
from multiple machines. Solutions:

1. **Designate one compiler.** One person (or CI) compiles. Others
   only search and capture.
2. **.gitignore the database.** Each person rebuilds their index from
   the compiled wiki.
3. **Use the server pattern.** Run sage-wiki on a shared server —
   only one process writes to the DB.

### "API costs are too high"

- Set `compiler.estimate_before: true` to see cost before compiling
- Use `default_tier: 1` — only compile what's actually queried
- Use batch mode for large initial compilations (50% discount)
- Mix cheap and quality models via the `models` config
- Enable prompt caching (on by default)
