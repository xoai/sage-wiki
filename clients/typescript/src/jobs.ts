/** Async job handle for the sage-wiki /v1 jobs API. */

import { JobFailedError, JobTimeoutError } from "./errors.js";
import type { JobData, JobStatus } from "./types.js";

export interface WaitOptions {
  /** Required — an unbounded wait is a compile-time error. */
  timeoutMs: number;
  pollIntervalMs?: number;
  signal?: AbortSignal;
}

const TERMINAL: ReadonlySet<JobStatus> = new Set(["done", "failed", "cancelled"]);

export class Job {
  jobId!: string;
  kind!: string;
  status!: JobStatus;
  submittedAt?: string;
  startedAt?: string;
  finishedAt?: string;
  progress?: unknown;
  result?: unknown;
  error?: JobData["error"];

  private readonly _refetch: (jobId: string, signal?: AbortSignal) => Promise<JobData>;

  constructor(data: JobData, refetch: (jobId: string, signal?: AbortSignal) => Promise<JobData>) {
    this._refetch = refetch;
    this._update(data);
  }

  static fromData(data: JobData, refetch: (jobId: string, signal?: AbortSignal) => Promise<JobData>): Job {
    return new Job(data, refetch);
  }

  private _update(d: JobData): void {
    this.jobId = d.job_id;
    this.kind = d.kind;
    this.status = d.status;
    this.submittedAt = d.submitted_at;
    this.startedAt = d.started_at;
    this.finishedAt = d.finished_at;
    this.progress = d.progress;
    this.result = d.result;
    this.error = d.error;
  }

  get terminal(): boolean {
    return TERMINAL.has(this.status);
  }

  async refresh(signal?: AbortSignal): Promise<this> {
    this._update(await this._refetch(this.jobId, signal));
    return this;
  }

  /** Poll until a terminal state. Rejects with JobTimeoutError on expiry,
   * JobFailedError (carrying the envelope) on failure, or the signal's
   * reason on abort. Cancelled RESOLVES — cancellation is not failure. */
  async waitUntilDone(options: WaitOptions): Promise<this> {
    const { timeoutMs, pollIntervalMs = 1000, signal } = options;
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      if (signal?.aborted) throw signal.reason ?? new Error("aborted");
      await this.refresh(signal);
      if (this.status === "failed") {
        const env = this.error;
        throw new JobFailedError(env?.code ?? "internal", env?.message ?? "job failed", env?.details);
      }
      if (this.terminal) return this;
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        throw new JobTimeoutError("timeout", `job ${this.jobId} not terminal after ${timeoutMs}ms`);
      }
      await sleep(Math.min(pollIntervalMs, remaining), signal);
    }
  }
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => {
      cleanup();
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(t);
      cleanup();
      reject(signal?.reason ?? new Error("aborted"));
    };
    const cleanup = () => signal?.removeEventListener("abort", onAbort);
    if (signal?.aborted) {
      clearTimeout(t);
      reject(signal.reason ?? new Error("aborted"));
      return;
    }
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
