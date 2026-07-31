/** SageWikiClient — zero-dependency typed client for sage-wiki's /v1 API.
 * Global fetch only; no Node built-ins (edge-runtime safe). */

import { InvalidArgument, SageWikiError, raiseForEnvelope } from "./errors.js";
import { Job } from "./jobs.js";
import type {
  Article, CallOptions, CompileDiff, CompileSubmit, Entity, GraphQueryResult,
  JobData, LintSubmit, ProvenanceResult, SearchOptions, SearchResults, Status,
  TextResult, TraverseResult,
} from "./types.js";

export interface ClientConfig {
  url?: string;
  token?: string;
  retries?: number;
  /** HTTP request timeout (distinct from job waits). Default 30000. */
  timeoutMs?: number;
  /** Injectable fetch (tests, exotic runtimes). Defaults to globalThis.fetch. */
  fetch?: typeof fetch;
}

const WRITE_METHODS = new Set(["POST", "PUT", "DELETE"]);

function envRead(key: string): string | undefined {
  // Guarded so edge runtimes without `process` parse this file safely.
  return typeof process !== "undefined" && process.env ? process.env[key] : undefined;
}

export class SageWikiClient {
  private readonly baseUrl: string;
  private readonly token?: string;
  private readonly retries: number;
  private readonly timeoutMs: number;
  private readonly _fetch: typeof fetch;

  constructor(config: ClientConfig = {}) {
    this.baseUrl = (config.url ?? envRead("SAGE_WIKI_URL") ?? "http://127.0.0.1:3333").replace(/\/+$/, "");
    const tok = config.token ?? envRead("SAGE_WIKI_TOKEN");
    if (tok) this.token = tok;
    this.retries = Math.max(0, config.retries ?? 0);
    this.timeoutMs = config.timeoutMs ?? 30000;
    this._fetch = config.fetch ?? fetch;
  }

  private async call(
    method: string,
    path: string,
    opts: {
      params?: Record<string, string | number | undefined>;
      body?: Record<string, unknown>;
      signal?: AbortSignal;
      idempotencyKey?: string;
    } = {},
  ): Promise<unknown> {
    const url = new URL(this.baseUrl + path);
    for (const [k, v] of Object.entries(opts.params ?? {})) {
      if (v !== undefined) url.searchParams.set(k, String(v));
    }
    const headers: Record<string, string> = {};
    // Content-Type only when a body is sent — on a cross-origin GET it would
    // force a CORS preflight, breaking the edge-deployable use case.
    if (opts.body !== undefined) headers["Content-Type"] = "application/json";
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (opts.idempotencyKey !== undefined) headers["Idempotency-Key"] = opts.idempotencyKey;

    // Fail fast on a pre-aborted caller signal (the reason must survive
    // signal composition).
    if (opts.signal?.aborted) throw opts.signal.reason ?? new Error("aborted");

    // Never auto-retry a non-idempotent write without an Idempotency-Key.
    const retryable = !WRITE_METHODS.has(method) || opts.idempotencyKey !== undefined;
    const attempts = 1 + (retryable ? Math.max(0, this.retries) : 0);

    for (let attempt = 0; attempt < attempts; attempt++) {
      const timeoutSignal = AbortSignal.timeout(this.timeoutMs);
      const composed = opts.signal
        ? composeSignals(opts.signal, timeoutSignal)
        : { signal: timeoutSignal, release: () => {} };
      let resp: Response;
      try {
        resp = await this._fetch(url.toString(), {
          method,
          headers,
          body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
          signal: composed.signal,
        });
      } catch (e) {
        composed.release();
        if (attempt + 1 < attempts && isTransportError(e)) {
          await backoff(attempt);
          continue;
        }
        throw e;
      }
      composed.release();
      if (resp.status === 503 && attempt + 1 < attempts) {
        await backoff(attempt);
        continue;
      }
      if (resp.status >= 400) {
        raiseForEnvelope(resp.status, await safeJson(resp));
      }
      if (resp.status === 204) return {};
      const parsed = await safeJson(resp);
      // A non-JSON 2xx (proxy HTML page, redirect target) is not a usable
      // API response — fail loudly rather than feed a string to parsers.
      if (typeof parsed === "string") {
        throw new SageWikiError(`http_${resp.status}`, `HTTP ${resp.status} with non-JSON body`);
      }
      return parsed;
    }
    throw new SageWikiError("unavailable", "request failed after retries");
  }

  // -- read ------------------------------------------------------------
  async search(query: string, opts: SearchOptions = {}): Promise<SearchResults> {
    const data = (await this.call("GET", "/v1/search", {
      params: {
        query,
        limit: opts.limit ?? 10,
        tags: opts.tags?.join(","),
        boost_tags: opts.boostTags?.join(","),
        channels: opts.channels?.join(","),
        expand: String(opts.expand ?? false),
        rerank: String(opts.rerank ?? false),
      },
      signal: opts.signal,
    })) as Record<string, unknown>;
    return parseSearchResults(data);
  }

  async readArticle(path: string, opts: CallOptions = {}): Promise<Article> {
    const d = (await this.call("GET", `/v1/articles/${encodePath(path)}`, opts)) as Record<string, unknown>;
    return { path: String(d.path ?? ""), content: String(d.content ?? "") };
  }

  async status(opts: CallOptions = {}): Promise<Status> {
    const d = (await this.call("GET", "/v1/status", opts)) as Record<string, unknown>;
    return {
      project: String(d.project ?? ""),
      mode: String(d.mode ?? ""),
      sourceCount: Number(d.source_count ?? 0),
      raw: d,
    };
  }

  async listEntities(opts: { type?: string } & CallOptions = {}): Promise<Entity[]> {
    const d = (await this.call("GET", "/v1/entities", { params: { type: opts.type }, signal: opts.signal })) as Record<string, unknown>;
    const list = (d.entities as Array<Record<string, unknown>>) ?? [];
    return list.map((e) => ({
      id: String(e.id ?? ""),
      type: String(e.type ?? ""),
      name: String(e.name ?? ""),
      articlePath: (e.article_path as string) || undefined,
    }));
  }

  async traverse(
    entity: string,
    opts: { relation?: string; direction?: string; depth?: number } & CallOptions = {},
  ): Promise<TraverseResult> {
    const d = await this.call("GET", `/v1/ontology/${encodePath(entity)}/traverse`, {
      params: { relation: opts.relation, direction: opts.direction ?? "outbound", depth: opts.depth ?? 1 },
      signal: opts.signal,
    });
    // Wire: bare array with relations, {"result": "null"} without.
    if (Array.isArray(d)) {
      return {
        entities: (d as Array<Record<string, unknown>>).map((e) => ({
          id: String(e.id ?? ""),
          type: String(e.type ?? ""),
          name: String(e.name ?? ""),
          articlePath: (e.article_path as string) || undefined,
        })),
        raw: d,
      };
    }
    return { entities: [], raw: (d as Record<string, unknown>).result };
  }

  async graphQuery(
    question: string,
    opts: { hops?: number; maxEdges?: number; asOf?: string; mode?: string } & CallOptions = {},
  ): Promise<GraphQueryResult> {
    const body: Record<string, unknown> = {
      question,
      hops: opts.hops ?? 2,
      max_edges: opts.maxEdges ?? 60,
      mode: opts.mode ?? "local",
    };
    if (opts.asOf !== undefined) body.as_of = opts.asOf;
    const d = (await this.call("POST", "/v1/graph/query", { body, signal: opts.signal })) as Record<string, unknown>;
    return {
      answer: String(d.answer ?? ""),
      cited: (d.cited as unknown[]) ?? [],
      seeds: (d.seeds as string[]) ?? [],
      truncated: Boolean(d.truncated),
    };
  }

  async provenance(opts: { source?: string; article?: string } & CallOptions = {}): Promise<ProvenanceResult> {
    const source = opts.source || undefined;
    const article = opts.article || undefined;
    if ((source === undefined) === (article === undefined)) {
      throw new InvalidArgument("invalid_argument", "exactly one of source or article is required");
    }
    const d = (await this.call("GET", "/v1/provenance", {
      params: { source, article },
      signal: opts.signal,
    })) as Record<string, unknown>;
    return {
      article: (d.article as string) || undefined,
      source: (d.source as string) || undefined,
      sources: d.sources ?? null,
      articles: (d.articles as Array<Record<string, unknown>>) ?? [],
      total: Number(d.total ?? 0),
    };
  }

  async compileDiff(opts: CallOptions = {}): Promise<CompileDiff> {
    const d = (await this.call("GET", "/v1/compile/diff", opts)) as Record<string, unknown>;
    return { diff: String(d.diff ?? "") };
  }

  // -- write -----------------------------------------------------------
  async addSource(path: string, opts: { type?: string } & CallOptions = {}): Promise<TextResult> {
    const body: Record<string, unknown> = { path };
    if (opts.type !== undefined) body.type = opts.type;
    return this.textResult(await this.call("POST", "/v1/sources", { body, ...opts }));
  }

  async writeSummary(source: string, content: string, opts: { concepts?: string[] } & CallOptions = {}): Promise<TextResult> {
    const body: Record<string, unknown> = { source, content };
    if (opts.concepts !== undefined) body.concepts = opts.concepts.join(",");
    return this.textResult(await this.call("PUT", "/v1/summaries", { body, ...opts }));
  }

  async writeArticle(concept: string, content: string, opts: CallOptions = {}): Promise<TextResult> {
    return this.textResult(
      await this.call("PUT", `/v1/articles/${encodeURIComponent(concept)}`, { body: { content }, ...opts }),
    );
  }

  async addEntity(id: string, type: string, name: string, opts: CallOptions = {}): Promise<TextResult> {
    return this.textResult(await this.call("POST", "/v1/ontology/entities", { body: { id, type, name }, ...opts }));
  }

  async addRelation(sourceId: string, targetId: string, relation: string, opts: CallOptions = {}): Promise<TextResult> {
    return this.textResult(
      await this.call("POST", "/v1/ontology/relations", {
        body: { source_id: sourceId, target_id: targetId, relation },
        ...opts,
      }),
    );
  }

  async learn(type: string, content: string, opts: { tags?: string[] } & CallOptions = {}): Promise<TextResult> {
    const body: Record<string, unknown> = { type, content };
    if (opts.tags !== undefined) body.tags = opts.tags.join(",");
    return this.textResult(await this.call("POST", "/v1/learnings", { body, ...opts }));
  }

  async capture(content: string, opts: { context?: string; tags?: string[] } & CallOptions = {}): Promise<TextResult> {
    const body: Record<string, unknown> = { content };
    if (opts.context !== undefined) body.context = opts.context;
    if (opts.tags !== undefined) body.tags = opts.tags.join(",");
    return this.textResult(await this.call("POST", "/v1/capture", { body, ...opts }));
  }

  async commit(message?: string, opts: CallOptions = {}): Promise<TextResult> {
    const body: Record<string, unknown> = {};
    if (message !== undefined) body.message = message;
    return this.textResult(await this.call("POST", "/v1/git/commit", { body, ...opts }));
  }

  // -- jobs ------------------------------------------------------------
  async compile(submit: CompileSubmit, opts: CallOptions = {}): Promise<Job> {
    let body: Record<string, unknown>;
    if (submit.topic !== undefined) {
      // Runtime mirror of the type union — JS callers bypassing the types
      // get the same local failure Python gives (and the server's 400).
      if (submit.dryRun !== undefined || submit.fresh !== undefined || submit.prune !== undefined) {
        throw new InvalidArgument("invalid_argument", "exactly one of 'topic' or compile flags expected");
      }
      body = { topic: submit.topic };
      if (submit.maxSources !== undefined) body.max_sources = submit.maxSources;
    } else {
      // The server 400s a flag-less body — dry_run is always serialized.
      body = { dry_run: submit.dryRun, fresh: submit.fresh ?? false, prune: submit.prune ?? false };
    }
    const d = (await this.call("POST", "/v1/jobs/compile", { body, ...opts })) as JobData;
    return Job.fromData(d, (id, signal) => this.fetchJob(id, signal));
  }

  async lint(submit: LintSubmit = {}, opts: CallOptions = {}): Promise<Job> {
    const body: Record<string, unknown> = { fix: submit.fix ?? false };
    if (submit.pass !== undefined) body.pass = submit.pass;
    const d = (await this.call("POST", "/v1/jobs/lint", { body, ...opts })) as JobData;
    return Job.fromData(d, (id, signal) => this.fetchJob(id, signal));
  }

  async job(jobId: string, opts: CallOptions = {}): Promise<Job> {
    const d = await this.fetchJob(jobId, opts.signal);
    return Job.fromData(d, (id, signal) => this.fetchJob(id, signal));
  }

  async jobs(opts: { status?: string } & CallOptions = {}): Promise<Job[]> {
    const d = (await this.call("GET", "/v1/jobs", { params: { status: opts.status }, signal: opts.signal })) as {
      jobs?: JobData[];
    };
    return (d.jobs ?? []).map((j) => Job.fromData(j, (id, signal) => this.fetchJob(id, signal)));
  }

  async cancelJob(jobId: string, opts: CallOptions = {}): Promise<Job> {
    const d = (await this.call("DELETE", `/v1/jobs/${encodeURIComponent(jobId)}`, opts)) as JobData;
    return Job.fromData(d, (id, signal) => this.fetchJob(id, signal));
  }

  private async fetchJob(jobId: string, signal?: AbortSignal): Promise<JobData> {
    return (await this.call("GET", `/v1/jobs/${encodeURIComponent(jobId)}`, { signal })) as JobData;
  }

  private textResult(d: unknown): TextResult {
    return { result: String((d as Record<string, unknown>).result ?? "") };
  }
}

function parseSearchResults(d: Record<string, unknown>): SearchResults {
  const raw = (d.results as Array<Record<string, unknown>> | null) ?? [];
  return {
    results: raw.map((x) => ({
      id: String(x.id ?? x.ID ?? ""),
      content: String(x.content ?? x.Content ?? ""),
      articlePath: (x.article_path ?? x.ArticlePath ?? undefined) as string | undefined,
      bm25Rank: Number(x.bm25_rank ?? x.BM25Rank ?? 0),
      vectorRank: Number(x.vector_rank ?? x.VectorRank ?? 0),
      rrfScore: Number(x.rrf_score ?? x.RRFScore ?? 0),
      finalScore: Number(x.final_score ?? x.FinalScore ?? 0),
      sourceDate: (x.source_date ?? x.SourceDate ?? undefined) as number | undefined,
      tags: (x.tags ?? x.Tags ?? []) as string[],
    })),
    uncompiledSources: Number(d.uncompiled_sources ?? 0),
    compileHint: (d.compile_hint as string) || undefined,
  };
}

function isTransportError(e: unknown): boolean {
  return e instanceof TypeError; // fetch network failures surface as TypeError
}

/** Encode a multi-segment path, preserving '/' separators. */
function encodePath(path: string): string {
  return path.split("/").map(encodeURIComponent).join("/");
}

/** AbortSignal.any is Node 20+ — the client targets Node 18, so compose
 * manually: the first signal to abort wins, with its reason. Listeners are
 * removed once the composed signal fires or `release` runs (call it when
 * the request settles) so a long-lived caller signal doesn't accumulate
 * them (MaxListenersExceededWarning after 10 requests). */
function composeSignals(a: AbortSignal, b: AbortSignal): { signal: AbortSignal; release: () => void } {
  const ctl = new AbortController();
  const cleanup = () => {
    a.removeEventListener("abort", onAbort);
    b.removeEventListener("abort", onAbort);
  };
  const onAbort = (ev: Event) => {
    const src = ev.target as AbortSignal;
    ctl.abort(src.reason);
    cleanup();
  };
  if (a.aborted) {
    ctl.abort(a.reason);
    return { signal: ctl.signal, release: () => {} };
  }
  if (b.aborted) {
    ctl.abort(b.reason);
    return { signal: ctl.signal, release: () => {} };
  }
  a.addEventListener("abort", onAbort, { once: true });
  b.addEventListener("abort", onAbort, { once: true });
  return { signal: ctl.signal, release: cleanup };
}

function backoff(attempt: number): Promise<void> {
  const ms = Math.min(100 * 2 ** attempt, 2000);
  return new Promise((r) => setTimeout(r, ms));
}

async function safeJson(resp: Response): Promise<unknown> {
  const text = await resp.text();
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}
