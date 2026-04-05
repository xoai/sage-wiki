# sage-wiki

An implementation of [Andrej Karpathy's idea](https://x.com/karpathy/status/2039805659525644595) for an LLM-compiled personal knowledge base.

Drop in your papers, articles, and notes. sage-wiki compiles them into a structured, interlinked wiki — with concepts extracted, cross-references discovered, and everything searchable.

- **Your sources in, a wiki out.** Add documents to a folder. The LLM reads, summarizes, extracts concepts, and writes interconnected articles.
- **Compounding knowledge.** Every new source enriches existing articles. The wiki gets smarter as it grows.
- **Works with your tools.** Opens natively in Obsidian. Connects to any LLM agent via MCP. Runs as a single binary — nothing to install beyond the API key.
- **Ask your wiki questions.** Search across everything with hybrid BM25 + semantic search, or ask natural language questions and get cited answers.

https://github.com/user-attachments/assets/c35ee202-e9df-4ccd-b520-8f057163ff26

*Dots on the outer boundary represent summaries of all documents in the knowledge base, while dots in the inner circle represent concepts extracted from the knowledge base, with links showing how those concepts connect to one another.*

## Install

```bash
go install github.com/xoai/sage-wiki/cmd/sage-wiki@latest
```

## Quickstart

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

## Commands

| Command | Description |
|---------|------------|
| `sage-wiki init [--vault]` | Initialize project (greenfield or vault overlay) |
| `sage-wiki compile [--watch] [--dry-run]` | Compile sources into wiki articles |
| `sage-wiki serve [--transport stdio\|sse]` | Start MCP server for LLM agents |
| `sage-wiki lint [--fix] [--pass name]` | Run linting passes |
| `sage-wiki search "query" [--tags ...]` | Hybrid search (BM25 + vector) |
| `sage-wiki query "question"` | Q&A against the wiki |
| `sage-wiki ingest <url\|path>` | Add a source |
| `sage-wiki status` | Wiki stats and health |
| `sage-wiki doctor` | Validate config and connectivity |

## Configuration

`config.yaml` is created by `sage-wiki init`. Key settings:

```yaml
project: my-research
api:
  provider: anthropic    # anthropic, openai, gemini, ollama
  api_key: ${API_KEY}    # env var expansion supported
models:
  summarize: claude-sonnet-4-20250514
  write: claude-opus-4-20250514
compiler:
  max_parallel: 4
  auto_commit: true
```

See the [spec](.sage/docs/spec.md) for full configuration reference.

## MCP Integration

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

## Architecture

- **Storage:** SQLite with FTS5 (BM25 search) + BLOB vectors (cosine similarity)
- **Ontology:** Typed entity-relation graph with BFS traversal and cycle detection
- **Search:** Reciprocal Rank Fusion (RRF) combining BM25 + vector + tag boost + recency decay
- **Compiler:** 5-pass pipeline (diff, summarize, extract concepts, write articles, images)
- **MCP:** 14 tools (5 read, 7 write, 2 compound) via stdio or SSE

Zero CGO. Pure Go. Cross-platform.

## License

MIT
