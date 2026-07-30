// Vercel AI SDK + sage-wiki: memory tools for an agent.
//
// Demonstrates exposing sage-wiki as AI SDK tools — search, graphQuery,
// provenance — via the zero-dependency `sagewiki` TS client. Because the
// client is global-fetch only with no Node built-ins, these exact tools
// deploy to edge runtimes (Cloudflare Workers, Deno, Vercel Edge) — a
// concrete advantage over subprocess-based integrations.
//
// The generation step is STUBBED (tools are invoked directly) so this runs
// with no model API key. Run against a live server:
//
//   eval "$(../../scripts/p4-fixture-server.sh)"   # or your own serve --ui
//   npm ci && npm start

import { tool } from "ai";
import { z } from "zod";
import { SageWikiClient } from "sagewiki";

const client = new SageWikiClient(); // SAGE_WIKI_URL / SAGE_WIKI_TOKEN from env

// The three tools an agent gets. Descriptions matter — they are what the
// model reads when deciding which tool to call.
export const searchWiki = tool({
  description:
    "Search the compiled wiki and raw sources. uncompiledSources > 0 means matching sources exist that are not compiled yet — call compileTopic next.",
  parameters: z.object({
    query: z.string().describe("natural-language query"),
    limit: z.number().int().min(1).max(50).default(10),
  }),
  execute: async ({ query, limit }) => client.search(query, { limit }),
});

export const graphQuery = tool({
  description:
    "Ask a question over the ontology graph (entities + relations), with provenance-aware answers. Prefer over searchWiki for relationship questions.",
  parameters: z.object({
    question: z.string(),
    hops: z.number().int().min(1).max(5).default(2),
  }),
  execute: async ({ question, hops }) => client.graphQuery(question, { hops }),
});

export const provenance = tool({
  description:
    "Show which sources back a compiled article (or which articles derive from a source). Evidence spans quote the compiled summary.",
  parameters: z.object({
    article: z.string().optional().describe("article concept, lowercase-hyphenated"),
    source: z.string().optional().describe("source path, e.g. raw/x.md"),
  }),
  execute: async ({ article, source }) => client.provenance({ article, source }),
});

async function main() {
  // STUB: a real agent passes these tools to generateText({ tools }) and the
  // model decides when to call them. Here we invoke them directly to stay
  // keyless and CI-safe.
  const query = process.argv[2] ?? "attention";

  const results = await searchWiki.execute(
    { query, limit: 5 },
    { toolCallId: "t1", messages: [] },
  );
  console.log(`[example] search('${query}'): ${results.results.length} results, uncompiledSources=${results.uncompiledSources}`);

  const gq = await graphQuery.execute(
    { question: query, hops: 1 },
    { toolCallId: "t2", messages: [] },
  );
  console.log(`[example] graphQuery: ${gq.answer.slice(0, 80)}`);

  const prov = await provenance.execute(
    { article: "attention" },
    { toolCallId: "t3", messages: [] },
  );
  console.log(`[example] provenance: total=${prov.total}`);

  if (results.results.length < 1) {
    console.error("[example] FAIL: no results retrieved");
    process.exit(1);
  }
  console.log("[example] PASS: retrieved >= 1 result");
}

main().catch((e) => {
  console.error("[example] FAIL:", e.message ?? e);
  process.exit(1);
});
