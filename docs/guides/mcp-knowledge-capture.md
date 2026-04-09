# Capturing Knowledge from AI Conversations

sage-wiki can act as an MCP (Model Context Protocol) server, letting you save knowledge directly from your AI conversations into your wiki. Instead of losing insights when a chat session ends, you can tell your AI to capture them.

## Setup

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

### ChatGPT

ChatGPT supports MCP servers via its remote server protocol. Point it to the SSE endpoint:

```bash
# Start sage-wiki in SSE mode
sage-wiki serve --transport sse --port 3333 --project /path/to/your/wiki
```

Then add `http://localhost:3333/sse` as an MCP server in ChatGPT settings.

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

### Other MCP Clients

Any client that supports MCP can connect via stdio or SSE:

```bash
# stdio (default)
sage-wiki serve --project /path/to/wiki

# SSE (network)
sage-wiki serve --transport sse --port 3333 --project /path/to/wiki
```

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

These become source files that the compiler weaves into your wiki's knowledge graph.
