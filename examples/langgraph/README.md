# LangGraph + sage-wiki example

> **Pre-1.0** — sage-wiki's API surface can change between releases. Pin the client version.

A minimal memory-backed agent graph: a retrieval node and a capture node
wired to a live sage-wiki server through the `sagewiki` Python client.

## What it demonstrates

- **Retrieval with the compile-on-demand signal.** `search()` returns
  `uncompiled_sources > 0` when matching sources exist that aren't compiled
  yet. The graph reacts by submitting a topic compile and waiting with an
  explicit timeout — the pattern that makes sage-wiki different from a
  plain vector store.
- **Capture.** The agent writes a note back through `capture()` (with an
  idempotency key), closing the read-capture loop.
- **Graph query.** A `graph_query()` call complements ranked retrieval with
  the ontology neighborhood.

## Run

```bash
pip install -r requirements.txt
eval "$(../../scripts/p4-fixture-server.sh)"   # fixture server, no LLM key needed
python main.py                                  # exits 0 with PASS
```

Against your own server: `sage-wiki serve --ui --port 3333` and export
`SAGE_WIKI_URL` / `SAGE_WIKI_TOKEN`.

## What it deliberately omits

- **A real LLM.** The generation node is a deterministic echo stub so the
  example runs with no API key. Swap `stub_llm` for your model call.
- **Auth setup depth.** Loopback is zero-config; non-loopback auth is the
  client's `token` / `SAGE_WIKI_TOKEN` — nothing more is covered here.
- **Error handling depth, retries, production concerns.** See the client
  README (`clients/python/README.md`) for the error taxonomy and retry
  policy; this example keeps the happy path visible.
- **Provenance display.** `client.provenance(article=...)` is one call away
  but left out to keep the graph two nodes.
