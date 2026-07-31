// Type-level tests: these blocks must COMPILE for the suite to pass.
// Each expect-error directive proves the compiler rejects that usage.
import type { CompileSubmit } from "../src/types.js";
import type { WaitOptions } from "../src/jobs.js";

const topicMode: CompileSubmit = { topic: "quantum", maxSources: 5 };
const fullMode: CompileSubmit = { dryRun: true, fresh: true };
const fullMinimal: CompileSubmit = { dryRun: false };

// @ts-expect-error — topic cannot mix with compile flags
const mixed: CompileSubmit = { topic: "x", dryRun: true };

// @ts-expect-error — the server 400s a flag-less body; dryRun is required
const flagless: CompileSubmit = {};

const waitOpts: WaitOptions = { timeoutMs: 5000 };

// @ts-expect-error — timeoutMs is required; unbounded waits are a type error
const noTimeout: WaitOptions = {};

// @ts-expect-error — timeoutMs must be a number
const wrongType: WaitOptions = { timeoutMs: "5s" };

export { topicMode, fullMode, fullMinimal, mixed, flagless, waitOpts, noTimeout, wrongType };
