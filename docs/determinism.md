# Determinism and the Compile Key

This document is the contract for SPEC-04: **identical inputs produce
byte-identical artifacts**, and **unchanged content is never recompiled**.

## Canonical serialization rules

Every serializer in this repository follows these rules. New code that
writes bytes to disk or to a stream follows them too.

1. **Sorted keys everywhere.** Any slice built by ranging a Go map that
   feeds bytes, prompts, merge decisions, or output order is sorted before
   use. `encoding/json` and `gopkg.in/yaml.v3` sort map keys on marshal;
   slices derived from maps are the caller's job (see the lint below).
2. **Fixed frontmatter field order.** Article frontmatter is emitted by a
   fixed-order template (`internal/compiler/write.go` `buildFrontmatter`):
   `concept, entity_type, aliases, sources, confidence`, then custom
   `article_fields` in declared order, then `created_at`. Aliases and
   sources are sets serialized as lists — emitted sorted ascending.
3. **Stable edge ordering.** Graph dumps order edges by (canonical source
   id, relation, target id, valid-from).
4. **LF endings.** Everything we write uses `\n`.
5. **UTC RFC3339 timestamps only in fields explicitly excluded from
   hashing** — the `created_at` family, below.

## The excluded field family

Timestamps are compile-clock values (`config.NowUTC`, which honors
`SOURCE_DATE_EPOCH`) and live only in these fields. They are the single
documented hash-excluded family:

- article/summary frontmatter `created_at` / `compiled_at`
- `CHANGELOG.md` entry headings
- `.manifest.json` `added_at` / `compiled_at` / `last_compiled` / `created_at`
- DB row timestamps (`created_at`, `updated_at`, lease/promotion columns,
  community/entity timestamps)
- `compile_id` (clock-derived)

With `SOURCE_DATE_EPOCH` pinned, all of them are fixed — the double-compile
byte-parity test (`internal/compiler/determinism_integration_test.go`)
pins it and `diff -r` comes back empty.

## Volatile operational state (excluded from byte-parity)

These files are wall-clock/goroutine-timing **operational state**, not
artifacts. The byte-parity test excludes exactly this list (keep the two
in sync — the test names this document):

- `.sage/usage.jsonl` — usage ledger (goroutine append order, wall clock)
- `.sage/jobs.jsonl` — serve-mode job log (RFC3339Nano + UnixNano ids)
- `.sage/lintlog/` — linter reports (wall-clock filenames + durations)
- `.sage/engine.lock` — flock token (pid + time)
- `.sage/batch-state.json` — batch checkpoint (provider batch ids)
- `.sage/compile-state.json`
- `.sage/wiki.db-wal`, `.sage/wiki.db-shm` — SQLite sidecars

Everything else — `wiki/`, `.manifest.json`, `.sage/wiki.db`, the SWVI
vector index, export streams — is byte-identical across identical
compiles, including with `max_parallel > 1` (parallel results are applied
in input order regardless of completion order).

## The compile key

Each tracked doc carries a content-addressed compile key in
`compile_items.compile_key` (with the component preimages in
`compile_key_parts`):

```
compile_key = SHA-256(
    "spec04/v1\n" +
    source_sha256_hex + "\n" +          // raw file bytes
    PipelineVersion + "\n" +            // internal/compiler, bumped by output-affecting code changes
    template_key + "\n" +               // 6 compile templates: name@version:sha256(effective bytes)[:16]
    model_key + "\n" +                  // resolved model per pass (summarize/extract/write/triples/resolve/communities)
    config_key + "\n" +                 // canonical JSON of the resolved output-affecting config subset
    embed_key                           // embed provider:model:dims
)
```

Tier < 3 docs (no LLM passes) use a reduced key: source, pipeline version,
embed key, and the chunk config subset (`chunk_size`,
`chunk_overlap_tokens`, `parsers`, `type_signals`).

The skip rule, in order: **R0** resume (incomplete tier-3 LLM passes
recompile regardless of keys) → **R1** `--force` → **R2** content changed
→ **R3** adopt (no stored key: compute + store, skip without recompiling —
the first run after upgrading costs nothing) → **R4** unchanged skip →
**R5** drift (pipeline / templates / models / config / embed — first
differing stored component). `sage-wiki compile --explain DOC` prints all
of this per doc; `sage-wiki diff` annotates drifted docs with their
class. Skips are reported in `CompileResult.Skipped`, in CLI output, and
as `compile_skip` engine events.

## Contributor duties (the parts automation can't enforce)

- **Bump `PipelineVersion`** (`internal/compiler/compilekey.go`) when a
  code change alters compile output: pipeline stages, **inline prompt
  strings** (e.g. the image-description prompt), serializers, defaults
  resolution.
- **Bump the template's version constant**
  (`internal/prompts/versions.go`) when editing an embedded template.
  The effective-content hash also catches edits that forgot the bump,
  and user overrides in `prompts/` — but the version is what `--explain`
  prints.
- **Disposition new config fields.** The reflection guard test
  (`TestConfigSubset_ReflectionGuard`) fails the build when a
  `config.Config` leaf lacks an entry in `subsetPolicy` — add it to the
  subset with a resolved value, or to the ignore list with a one-line
  justification.
- **Sort before you write.** `scripts/check-determinism.sh` is the
  best-effort grep lint (below).

## Determinism lint

`scripts/check-determinism.sh` greps `internal/` for `range` over maps
near serializers/writers and fails on any hit not allowlisted in
`scripts/determinism-allowlist.txt` (one line per verified-safe site,
with its justification). It is a **best-effort static check**: it cannot
see dataflow (a map-range two functions away from the writer), and it
cannot prove a sorted site is sorted by the RIGHT key. It is a tripwire,
not a proof — the proofs are the double-compile tests. Run it via
`--self-test` to see it catch a planted offender.

## Honest limits

- **LLM providers are not bit-stable across their own releases.**
  Determinism here means *our* pipeline adds no nondeterminism: sorted
  iteration, temperature 0 by default (`compiler.temperature` overrides),
  input-order application, one SDE-aware clock. The model identity is in
  the key, so provider drift surfaces as a hash change (`models` drift),
  not silent divergence.
- **Frontmatter/alias order inside LLM prose** (article bodies, the
  manifest's internal alias ordering) is LLM-derived content, like prose
  itself — not canonicalized. Byte-parity across identical compiles rests
  on identical LLM outputs under replay/pinned temperature.
- **A live export** (`engine.Export` over an open DB) copies the DB as-is;
  a concurrent compile may leave it inconsistent. Export bytes are
  normalized (no mtimes/uids), but live-export byte-parity is not claimed.
- **`secure_delete` is forward-only** (`PRAGMA secure_delete=ON`, D7):
  freed-cell garbage in DB files written before SPEC-04 stays where it is
  until those pages are rewritten.

## Adoption and provenance

The first compile after upgrading to SPEC-04 **adopts** keys for unchanged
docs without recompiling (R3). Adopted artifacts were produced by
pre-SPEC-04 code — provider-default temperature, pre-sort prompts — so an
adopted doc's key asserts the *current* pipeline's identity over artifacts
the current pipeline did not make. For dedup this is sound and deliberate:
the never-re-bill pledge applies from run one. But adopted artifacts are
not reproducible artifacts until they next genuinely recompile (content,
model, or config drift; or a deliberate re-baseline). If you want a
provably deterministic tree, run `sage-wiki compile --force` once — every
key is then stamped by the deterministic pipeline. Any future
"reproduce-and-verify" tooling should treat adopted docs as
non-reproducing until re-baselined.

## What determinism does not cover

- **Non-compile writers.** MCP capture, the session scribe, on-demand
  query paths, and trust review files stamp wall-clock timestamps from
  their own flows (no injected clock yet) — they are operational state,
  not compile artifacts. A byte-parity comparison assumes no non-compile
  writes between the two compiles.
- **Other writers of the same DB.** `PRAGMA secure_delete=ON` is set by
  the current binary's writer connection; an old binary or a hand-run
  `sqlite3` writing the same file reintroduces freed-cell garbage. The
  parity guarantee holds when every writer runs the new binary.
