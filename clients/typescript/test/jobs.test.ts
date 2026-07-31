import { test } from "node:test";
import assert from "node:assert/strict";
import { Job } from "../src/jobs.js";
import { JobFailedError, JobTimeoutError } from "../src/errors.js";
import type { JobData } from "../src/types.js";

const PAYLOADS: Record<string, JobData> = {
  pending: { job_id: "j1", kind: "compile", status: "pending" },
  running: { job_id: "j1", kind: "compile", status: "running" },
  done: { job_id: "j1", kind: "compile", status: "done", result: { sources_compiled: 3 } },
  failed: {
    job_id: "j1",
    kind: "compile",
    status: "failed",
    error: { code: "internal", message: "LLM provider returned 429" },
  },
  cancelled: { job_id: "j1", kind: "compile", status: "cancelled" },
};

function makeJob(sequence: string[]) {
  let calls = 0;
  const refetch = async () => PAYLOADS[sequence[Math.min(calls++, sequence.length - 1)]];
  return { job: new Job(PAYLOADS.pending, refetch), calls: () => calls };
}

test("waitUntilDone polls until done", async () => {
  const { job } = makeJob(["running", "running", "done"]);
  const out = await job.waitUntilDone({ timeoutMs: 5000, pollIntervalMs: 1 });
  assert.equal(out.status, "done");
  assert.deepEqual(out.result, { sources_compiled: 3 });
});

test("waitUntilDone rejects JobTimeoutError on expiry", async () => {
  const { job } = makeJob(["running"]);
  await assert.rejects(() => job.waitUntilDone({ timeoutMs: 20, pollIntervalMs: 5 }), JobTimeoutError);
});

test("waitUntilDone rejects JobFailedError with the envelope", async () => {
  const { job } = makeJob(["failed"]);
  await assert.rejects(
    () => job.waitUntilDone({ timeoutMs: 100, pollIntervalMs: 1 }),
    (e: unknown) => {
      assert.ok(e instanceof JobFailedError);
      assert.equal((e as JobFailedError).code, "internal");
      assert.match((e as JobFailedError).message, /429/);
      return true;
    },
  );
});

test("cancelled resolves, does not reject", async () => {
  const { job } = makeJob(["cancelled"]);
  const out = await job.waitUntilDone({ timeoutMs: 100, pollIntervalMs: 1 });
  assert.equal(out.status, "cancelled");
});

test("abort mid-wait rejects with the signal reason", async () => {
  const { job } = makeJob(["running"]);
  const ctl = new AbortController();
  setTimeout(() => ctl.abort(new Error("user stop")), 15);
  await assert.rejects(
    () => job.waitUntilDone({ timeoutMs: 5000, pollIntervalMs: 5, signal: ctl.signal }),
    /user stop/,
  );
});

test("refresh mutates and returns self", async () => {
  const { job } = makeJob(["running"]);
  const out = await job.refresh();
  assert.equal(out, job);
  assert.equal(job.status, "running");
});
