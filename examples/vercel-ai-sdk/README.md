# Vercel AI SDK + sage-wiki example

> **Pre-1.0** — sage-wiki's API surface can change between releases. Pin the client version.

Exposes sage-wiki as three AI SDK tools — `searchWiki`, `graphQuery`,
`provenance` — through the zero-dependency `sagewiki` TS client.

## What it demonstrates

- **Tool definitions with the AI SDK `tool()` shape**, descriptions written
  for the model (including when to prefer `graphQuery` over `searchWiki`).
- **The compile-on-demand signal** — `uncompiledSources > 0` tells the agent
  a topic compile is worth submitting (see the LangGraph example for the
  full submit-and-wait pattern).
- **Edge deployability.** The client is global-`fetch` only with zero
  runtime dependencies and no Node built-ins, so these tools run on
  Cloudflare Workers, Deno, Bun, and Vercel Edge — not just Node servers.

## Run

```bash
npm ci
eval "$(../../scripts/p4-fixture-server.sh)"   # fixture server, no LLM key needed
npm start                                      # exits 0 with PASS
```

Against your own server: `sage-wiki serve --ui --port 3333` and export
`SAGE_WIKI_URL` / `SAGE_WIKI_TOKEN`.

## What it deliberately omits

- **A real model call.** A production agent passes the tools to
  `generateText({ model, tools })`; here the tools are invoked directly so
  the example runs with no API key.
- **Streaming and multi-step tool loops.** One call per tool keeps the
  wiring visible.
- **Auth setup depth, error handling, production concerns.** See
  `clients/typescript/README.md` for the error taxonomy, AbortSignal, and
  retry policy.
