/** Error taxonomy for the sage-wiki /v1 API — branch on `code`, never on `message`. */

export type ErrorDetails = Record<string, unknown>;

export class SageWikiError extends Error {
  readonly code: string;
  readonly details?: ErrorDetails;

  constructor(code: string, message: string, details?: ErrorDetails) {
    super(message);
    this.name = new.target.name;
    this.code = code;
    if (details !== undefined) this.details = details;
  }
}

export class InvalidArgument extends SageWikiError {}
export class Unauthenticated extends SageWikiError {}
export class Forbidden extends SageWikiError {}
export class NotFound extends SageWikiError {}

export class Conflict extends SageWikiError {
  /** The conflicting job's ID, when the server supplied one (compile 409). */
  get activeJobId(): string | undefined {
    const v = this.details?.["active_job_id"];
    return typeof v === "string" ? v : undefined;
  }
}

export class FeatureDisabled extends SageWikiError {
  /** The two real 412 causes: as_of queries need ontology.temporal.enabled;
   * mode=global needs ontology.communities.enabled. */
  static readonly hint =
    "as_of queries need ontology.temporal.enabled; mode=global needs ontology.communities.enabled";
}
export class PayloadTooLarge extends SageWikiError {}
export class RateLimited extends SageWikiError {}
export class InternalError extends SageWikiError {}
export class Unavailable extends SageWikiError {}
export class JobTimeoutError extends SageWikiError {}
export class JobFailedError extends SageWikiError {}

type ErrorClass = new (code: string, message: string, details?: ErrorDetails) => SageWikiError;

const CODE_CLASSES: Record<string, ErrorClass> = {
  invalid_argument: InvalidArgument,
  unauthenticated: Unauthenticated,
  forbidden: Forbidden,
  not_found: NotFound,
  conflict: Conflict,
  feature_disabled: FeatureDisabled,
  payload_too_large: PayloadTooLarge,
  rate_limited: RateLimited,
  internal: InternalError,
  unavailable: Unavailable,
};

interface Envelope {
  error?: { code?: unknown; message?: unknown; details?: unknown };
}

/**
 * Raise the mapped error for a non-2xx response. Unknown codes map to the
 * base class (forward-compatible). Bodies that aren't the error envelope
 * (proxy HTML pages, etc.) map to `http_<status>` — never a parse crash.
 */
export function raiseForEnvelope(status: number, body: unknown): never {
  if (typeof body === "object" && body !== null) {
    const env = (body as Envelope).error;
    if (env && typeof env.code === "string") {
      const code = env.code;
      const message = typeof env.message === "string" ? env.message : "";
      const details =
        typeof env.details === "object" && env.details !== null
          ? (env.details as ErrorDetails)
          : undefined;
      const Cls = CODE_CLASSES[code] ?? SageWikiError;
      throw new Cls(code, message, details);
    }
  }
  throw new SageWikiError(`http_${status}`, `HTTP ${status} with non-JSON error body`);
}
