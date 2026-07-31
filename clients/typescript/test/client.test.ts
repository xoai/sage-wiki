import { test } from "node:test";
import assert from "node:assert/strict";
import { SageWikiClient } from "../src/client.js";

interface Captured {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

function mockClient(payload: unknown = {}, status = 200) {
  const calls: Captured[] = [];
  const fetchMock = async (input: unknown, init?: RequestInit) => {
    calls.push({
      url: String(input),
      method: init?.method ?? "GET",
      headers: Object.fromEntries(new Headers(init?.headers) as unknown as Iterable<readonly [string, string]>),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return new Response(JSON.stringify(payload), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  };
  const client = new SageWikiClient({ url: "http://fixture:3333", token: "tok", fetch: fetchMock as typeof fetch });
  return { calls, client };
}

const JOB = { job_id: "j", kind: "compile", status: "pending", submitted_at: "t" };

test("search builds query params with comma-joined lists", async () => {
  const { calls, client } = mockClient({ results: [] });
  await client.search("attention", { tags: ["a", "b"], channels: ["bm25"], limit: 3, expand: true });
  const u = new URL(calls[0].url);
  assert.equal(u.pathname, "/v1/search");
  assert.equal(u.searchParams.get("query"), "attention");
  assert.equal(u.searchParams.get("tags"), "a,b");
  assert.equal(u.searchParams.get("channels"), "bm25");
  assert.equal(u.searchParams.get("limit"), "3");
  assert.equal(u.searchParams.get("expand"), "true");
  assert.equal(u.searchParams.get("rerank"), "false");
  assert.equal(calls[0].headers["authorization"], "Bearer tok");
});

test("readArticle escapes the path", async () => {
  const { calls, client } = mockClient({ path: "concepts/x.md", content: "c" });
  await client.readArticle("concepts/x.md");
  assert.equal(new URL(calls[0].url).pathname, "/v1/articles/concepts/x.md");
});

test("status / entities / traverse / compileDiff", async () => {
  const { calls, client } = mockClient({ entities: [] });
  await client.status();
  await client.listEntities({ type: "concept" });
  await client.traverse("attention", { depth: 2, direction: "both" });
  await client.compileDiff();
  assert.equal(new URL(calls[0].url).pathname, "/v1/status");
  assert.equal(new URL(calls[1].url).searchParams.get("type"), "concept");
  assert.equal(new URL(calls[2].url).pathname, "/v1/ontology/attention/traverse");
  assert.equal(new URL(calls[2].url).searchParams.get("depth"), "2");
  assert.equal(new URL(calls[3].url).pathname, "/v1/compile/diff");
});

test("graphQuery posts the body with defaults", async () => {
  const { calls, client } = mockClient({ answer: "", cited: [], seeds: [], truncated: false });
  await client.graphQuery("what is attention", { hops: 1 });
  assert.equal(calls[0].method, "POST");
  assert.deepEqual(calls[0].body, { question: "what is attention", hops: 1, max_edges: 60, mode: "local" });
});

test("graphQuery includes as_of only when set", async () => {
  const { calls, client } = mockClient({ answer: "", cited: [], seeds: [], truncated: false });
  await client.graphQuery("q", { asOf: "2026-01-01T00:00:00Z" });
  assert.equal((calls[0].body as Record<string, unknown>).as_of, "2026-01-01T00:00:00Z");
});

test("provenance validates exactly-one locally", async () => {
  const { calls, client } = mockClient();
  await assert.rejects(() => client.provenance({}), /exactly one/);
  await assert.rejects(() => client.provenance({ source: "a", article: "b" }), /exactly one/);
  assert.equal(calls.length, 0);
  await client.provenance({ article: "x" });
  assert.equal(new URL(calls[0].url).searchParams.get("article"), "x");
});

test("write methods send snake_case bodies", async () => {
  const { calls, client } = mockClient({ result: "ok" });
  await client.addSource("raw/x.md");
  await client.writeSummary("s.md", "content", { concepts: ["a", "b"] });
  await client.writeArticle("attention", "md");
  await client.addEntity("e1", "concept", "Name");
  await client.addRelation("a", "b", "relates_to");
  await client.learn("gotcha", "content", { tags: ["x"] });
  await client.capture("c", { context: "ctx", tags: ["t1", "t2"] });
  await client.commit("msg");
  assert.deepEqual(calls[0].body, { path: "raw/x.md" });
  assert.deepEqual(calls[1].body, { source: "s.md", content: "content", concepts: "a,b" });
  assert.deepEqual(calls[2].body, { content: "md" });
  assert.equal(new URL(calls[2].url).pathname, "/v1/articles/attention");
  assert.deepEqual(calls[3].body, { id: "e1", type: "concept", name: "Name" });
  assert.deepEqual(calls[4].body, { source_id: "a", target_id: "b", relation: "relates_to" });
  assert.deepEqual(calls[5].body, { type: "gotcha", content: "content", tags: "x" });
  assert.deepEqual(calls[6].body, { content: "c", context: "ctx", tags: "t1,t2" });
  assert.deepEqual(calls[7].body, { message: "msg" });
});

test("compile topic mode vs full mode bodies", async () => {
  const { calls, client } = mockClient(JOB);
  await client.compile({ topic: "quantum", maxSources: 5 });
  await client.compile({ dryRun: true });
  assert.deepEqual(calls[0].body, { topic: "quantum", max_sources: 5 });
  assert.deepEqual(calls[1].body, { dry_run: true, fresh: false, prune: false });
});

test("lint body", async () => {
  const { calls, client } = mockClient(JOB);
  await client.lint({ pass: "connections", fix: true });
  assert.deepEqual(calls[0].body, { pass: "connections", fix: true });
});

test("job / jobs / cancelJob", async () => {
  const { calls, client } = mockClient(JOB);
  await client.job("id-1");
  await client.jobs({ status: "running" });
  await client.cancelJob("id-2");
  assert.equal(new URL(calls[0].url).pathname, "/v1/jobs/id-1");
  assert.equal(new URL(calls[1].url).searchParams.get("status"), "running");
  assert.equal(calls[2].method, "DELETE");
});

test("idempotency key forwarded verbatim", async () => {
  const { calls, client } = mockClient({ result: "ok" });
  await client.capture("c", { idempotencyKey: "Key-123_ABC" });
  assert.equal(calls[0].headers["idempotency-key"], "Key-123_ABC");
});

test("no token sends no authorization header, incl. job polls", async () => {
  const calls: Captured[] = [];
  const fetchMock = async (input: unknown, init?: RequestInit) => {
    calls.push({
      url: String(input),
      method: init?.method ?? "GET",
      headers: Object.fromEntries(new Headers(init?.headers) as unknown as Iterable<readonly [string, string]>),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return new Response(JSON.stringify(JOB), { status: 200 });
  };
  const client = new SageWikiClient({ url: "http://fixture:3333", fetch: fetchMock as typeof fetch });
  await client.job("j");
  await client.jobs();
  await client.status();
  assert.equal(calls.length, 3);
  for (const call of calls) {
    assert.ok(!("authorization" in call.headers), "authorization header sent without token");
  }
});

test("zero-hit search maps to empty results with count 0", async () => {
  const { client } = mockClient({ results: [] });
  const r = await client.search("nothing");
  assert.deepEqual(r.results, []);
  assert.equal(r.uncompiledSources, 0);
});

test("null results tolerated (legacy pipeline)", async () => {
  const { client } = mockClient({ results: null });
  const r = await client.search("nothing");
  assert.deepEqual(r.results, []);
});

test("PascalCase wire keys canonicalized", async () => {
  const { client } = mockClient({
    results: [
      {
        ID: "attention.md",
        Content: "Self-attention…",
        ArticlePath: "wiki/summaries/attention.md",
        BM25Rank: 1,
        VectorRank: 0,
        RRFScore: 0.011,
        FinalScore: 1.0,
        SourceDate: 1785449267,
        Tags: ["article"],
      },
    ],
    uncompiled_sources: 2,
  });
  const r = await client.search("attention");
  assert.equal(r.uncompiledSources, 2);
  const item = r.results[0];
  assert.equal(item.id, "attention.md");
  assert.equal(item.articlePath, "wiki/summaries/attention.md");
  assert.equal(item.bm25Rank, 1);
  assert.equal(item.finalScore, 1.0);
  assert.deepEqual(item.tags, ["article"]);
});

test("503 retried up to retries", async () => {
  let n = 0;
  const fetchMock = async () => {
    n++;
    if (n < 3) {
      return new Response(JSON.stringify({ error: { code: "unavailable", message: "down" } }), { status: 503 });
    }
    return new Response(JSON.stringify({ results: [] }), { status: 200 });
  };
  const client = new SageWikiClient({ url: "http://f", retries: 2, fetch: fetchMock as typeof fetch });
  await client.search("x");
  assert.equal(n, 3);
});

test("non-idempotent POST without key never retried", async () => {
  let n = 0;
  const fetchMock = async () => {
    n++;
    return new Response(JSON.stringify({ error: { code: "unavailable", message: "down" } }), { status: 503 });
  };
  const client = new SageWikiClient({ url: "http://f", retries: 3, fetch: fetchMock as typeof fetch });
  await assert.rejects(() => client.capture("c"));
  assert.equal(n, 1);
});

test("request timeout rejects a hanging fetch", async () => {
  // The guard timer keeps the event loop alive: AbortSignal.timeout's
  // internal timer is unref'd on Node 18/20, and a pending promise with no
  // live handles makes node --test report "event loop has already
  // resolved". If the client's timeout works, abort fires at 50ms.
  const fetchMock = (input: unknown, init?: RequestInit) =>
    new Promise<Response>((_, reject) => {
      const guard = setTimeout(() => reject(new Error("mock hung — client timeout never fired")), 5000);
      init?.signal?.addEventListener("abort", () => {
        clearTimeout(guard);
        reject(init.signal?.reason);
      });
    });
  const client = new SageWikiClient({ url: "http://f", timeoutMs: 50, fetch: fetchMock as typeof fetch });
  await assert.rejects(() => client.status(), /timed out|abort/i);
});

test("pre-aborted signal rejects immediately", async () => {
  const { client } = mockClient({});
  const ctl = new AbortController();
  ctl.abort(new Error("user abort"));
  await assert.rejects(() => client.status({ signal: ctl.signal }), /user abort/);
});

test("traverse handles bare-array wire shape", async () => {
  const { client } = mockClient([{ id: "transformer", type: "concept", name: "Transformer" }]);
  const r = await client.traverse("attention");
  assert.equal(r.entities.length, 1);
  assert.equal(r.entities[0].id, "transformer");
});

test("traverse handles null-result wire shape", async () => {
  const { client } = mockClient({ result: "null" });
  const r = await client.traverse("attention");
  assert.deepEqual(r.entities, []);
});

test("provenance source direction reads the articles key", async () => {
  const { client } = mockClient({
    source: "paper.pdf",
    articles: [{ concept: "attention", article_path: "wiki/concepts/attention.md" }],
    total: 1,
  });
  const r = await client.provenance({ source: "paper.pdf" });
  assert.equal(r.source, "paper.pdf");
  assert.equal(r.total, 1);
  assert.equal(r.articles[0].concept, "attention");
});

test("provenance rejects empty strings locally", async () => {
  const { calls, client } = mockClient();
  await assert.rejects(() => client.provenance({ source: "" }), /exactly one/);
  assert.equal(calls.length, 0);
});

test("main entry imports no Node built-ins", async () => {
  const { readFileSync, readdirSync } = await import("node:fs");
  const { join } = await import("node:path");
  // Scan the REAL source tree — the compiled layout has no .ts files, and a
  // test that scans nothing passes vacuously.
  const srcDir = join(process.cwd(), "src");
  const files = readdirSync(srcDir).filter((f) => f.endsWith(".ts"));
  assert.ok(files.length >= 4, `expected to scan >=4 source files, found ${files.length}`);
  const allowlist: string[] = [];
  for (const f of files) {
    const src = readFileSync(join(srcDir, f), "utf8");
    for (const m of src.matchAll(/from\s+["']([^"']+)["']/g)) {
      const spec = m[1];
      if (spec.startsWith("node:") || ["fs", "path", "os", "process", "child_process"].includes(spec)) {
        if (!allowlist.includes(spec)) {
          assert.fail(`${f} imports Node built-in ${spec}`);
        }
      }
    }
  }
});
