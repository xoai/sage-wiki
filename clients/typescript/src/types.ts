/** Wire types for the sage-wiki /v1 API (hand-maintained; proven by the
 * contract test against a live server). */

/** Search result item. The wire carries PascalCase keys (untagged server
 * struct, pre-1.0); the client canonicalizes to camelCase. */
export interface SearchResultItem {
  id: string;
  content: string;
  articlePath?: string;
  bm25Rank: number;
  vectorRank: number;
  rrfScore: number;
  finalScore: number;
  sourceDate?: number;
  tags: string[];
}

export interface SearchResults {
  results: SearchResultItem[];
  /** Count of matching sources not yet fully compiled — the
   * compile-on-demand signal (> 0 means `compile({topic})` may help). */
  uncompiledSources: number;
  compileHint?: string;
}

export interface Article {
  path: string;
  content: string;
}

export interface Status {
  project: string;
  mode: string;
  sourceCount: number;
  raw: Record<string, unknown>;
}

export interface Entity {
  id: string;
  type: string;
  name: string;
  articlePath?: string;
}

export interface TraverseResult {
  /** Traversed entities. The wire is a bare array when relations exist,
   * {"result": "null"} (a string) when none do — both handled. */
  entities: Entity[];
  raw: unknown;
}

export interface GraphQueryResult {
  answer: string;
  cited: unknown[];
  seeds: string[];
  truncated: boolean;
}

export interface ProvenanceResult {
  /** Set for article queries; wire: {"article","sources","total"}. */
  article?: string;
  /** Set for source queries; wire: {"source","articles","total"}. */
  source?: string;
  sources: unknown;
  articles: Array<Record<string, unknown>>;
  total: number;
}

export interface TextResult {
  result: string;
}

export interface CompileDiff {
  diff: string;
}

export type JobStatus = "pending" | "running" | "done" | "failed" | "cancelled";
export type JobKind = "compile" | "compile_topic" | "lint";

export interface JobData {
  job_id: string;
  kind: JobKind;
  status: JobStatus;
  submitted_at?: string;
  started_at?: string;
  finished_at?: string;
  progress?: unknown;
  result?: unknown;
  error?: { code: string; message: string; details?: Record<string, unknown> };
}

/**
 * Compile submit body — the flagship discriminated union. Topic mode and
 * full-compile flags are mutually exclusive, and full mode requires
 * `dryRun` explicitly (the server 400s a flag-less body).
 */
export type CompileSubmit =
  | { topic: string; maxSources?: number; dryRun?: never; fresh?: never; prune?: never }
  | { topic?: never; dryRun: boolean; fresh?: boolean; prune?: boolean };

export interface LintSubmit {
  pass?: string;
  fix?: boolean;
}

export interface SearchOptions {
  tags?: string[];
  boostTags?: string[];
  limit?: number;
  channels?: string[];
  expand?: boolean;
  rerank?: boolean;
  signal?: AbortSignal;
  idempotencyKey?: string;
}

export interface CallOptions {
  signal?: AbortSignal;
  idempotencyKey?: string;
}
