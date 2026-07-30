import { test } from "node:test";
import assert from "node:assert/strict";
import { SageWikiClient } from "../src/client.js";
import { InvalidArgument, NotFound, Unauthenticated } from "../src/errors.js";

const LIVE = !!(
  typeof process !== "undefined" && process.env && process.env.SAGE_WIKI_URL
);
const client = new SageWikiClient(); // env config

test("status", { skip: !LIVE }, async () => {
  const s = await client.status();
  assert.ok(s.project);
});

test("search returns the seeded result with typed items", { skip: !LIVE }, async () => {
  const r = await client.search("attention", { limit: 3 });
  assert.ok(r.results.length >= 1);
  const item = r.results[0];
  assert.equal(typeof item.id, "string");
  assert.equal(typeof item.content, "string");
  assert.equal(typeof item.finalScore, "number");
  assert.ok(r.results.some((i) => i.content.toLowerCase().includes("attention")));
});

test("readArticle", { skip: !LIVE }, async () => {
  const a = await client.readArticle("concepts/attention.md");
  assert.match(a.content, /Attention/);
});

test("listEntities", { skip: !LIVE }, async () => {
  const entities = await client.listEntities({ type: "concept" });
  assert.ok(entities.some((e) => e.id === "attention"));
});

test("traverse", { skip: !LIVE }, async () => {
  await client.traverse("attention", { depth: 1 });
});

test("graphQuery", { skip: !LIVE }, async () => {
  const r = await client.graphQuery("what is attention", { hops: 1 });
  assert.equal(typeof r.answer, "string");
});

test("provenance", { skip: !LIVE }, async () => {
  await client.provenance({ article: "attention" });
});

test("compileDiff", { skip: !LIVE }, async () => {
  assert.equal(typeof (await client.compileDiff()).diff, "string");
});

test("writes roundtrip + idempotent replay", { skip: !LIVE }, async () => {
  const r1 = await client.capture("ts contract capture", { idempotencyKey: "ts-cap-idem" });
  const r2 = await client.capture("ts contract capture", { idempotencyKey: "ts-cap-idem" });
  assert.equal(r1.result, r2.result);
  const w = await client.writeSummary("ts-contract.md", "TS contract summary about recursion.", {
    idempotencyKey: "ts-sum-1",
  });
  assert.ok(w.result);
  const e = await client.addEntity("memoization", "concept", "Memoization", {
    idempotencyKey: "ts-ent-1",
  });
  assert.ok(e.result);
});

test("commit", { skip: !LIVE }, async () => {
  await client.commit("ts contract commit");
});

test("lint job flow", { skip: !LIVE }, async () => {
  const job = await client.lint({ fix: false });
  assert.ok(job.jobId);
  const done = await job.waitUntilDone({ timeoutMs: 120000, pollIntervalMs: 500 });
  // lint may fail without an LLM key; the flow is what matters.
  assert.ok(["done", "failed"].includes(done.status));
  const fetched = await client.job(job.jobId);
  assert.equal(fetched.jobId, job.jobId);
  const list = await client.jobs();
  assert.ok(list.some((j) => j.jobId === job.jobId));
});

test("compile dry-run job", { skip: !LIVE }, async () => {
  const job = await client.compile({ dryRun: true });
  const done = await job.waitUntilDone({ timeoutMs: 120000, pollIntervalMs: 500 });
  assert.ok(["done", "failed"].includes(done.status));
});

test("unknown job → NotFound", { skip: !LIVE }, async () => {
  await assert.rejects(() => client.job("00000000-0000-4000-8000-000000000000"), NotFound);
});

test("mixed compile body → server InvalidArgument", { skip: !LIVE }, async () => {
  // Raw fetch: the client's union rightly prevents this at compile time;
  // this proves the server enforces the same rule and the error maps.
  const resp = await fetch(`${process.env.SAGE_WIKI_URL}/v1/jobs/compile`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${process.env.SAGE_WIKI_TOKEN}`,
    },
    body: JSON.stringify({ topic: "x", dry_run: true }),
  });
  assert.equal(resp.status, 400);
  const body = await resp.json();
  const { raiseForEnvelope } = await import("../src/errors.js");
  assert.throws(() => raiseForEnvelope(resp.status, body), InvalidArgument);
});

test("bad token → Unauthenticated", { skip: !LIVE }, async () => {
  const bad = new SageWikiClient({
    url: process.env.SAGE_WIKI_URL,
    token: "wrong-token",
  });
  await assert.rejects(() => bad.status(), Unauthenticated);
});
