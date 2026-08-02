# Parity Suite (SPEC-09)

The golden-corpus parity suite proves on every PR that observable behavior
is unchanged unless a spec explicitly authorizes a change, and that a
workspace is fully portable (export → open elsewhere → identical answers).

Everything runs offline: `internal/parity` builds `testdata/golden-corpus/`
into a workspace against a record/replay HTTP stub
(`testdata/fixtures/openai/`), then checks four golden contracts against
`testdata/golden/`:

- **byte parity** — normalized `wiki/` tree hashes + normalized manifest +
  compile_items/counts + config hash (`byte-parity.json`)
- **graph parity** — canonical edge dump incl. evidence/confidence/validity
  (`graph.jsonl`), plus the AsOf view (`graph-asof.json`)
- **search parity** — 33 committed queries (lexical/semantic/graph-hop/
  coverage-gap classes, plus bm25/vector/graph channel scoping) with
  exact doc+rank+score (`search.json`); temporal as_of parity lives in
  `graph-asof.json`
- **round-trip** — export → untar → read-only open → identical answers;
  a corrupted byte inside a wiki/ file must fail byte parity

Determinism is pinned by construction: corpus mtimes at a fixed epoch,
`SOURCE_DATE_EPOCH` for compile timestamps, `max_parallel: 1`, and a
far-future search clock (recency bonus underflows to exactly 0).
`usage.jsonl` is excluded (the event log is timestamped by design); the
DB is byte-parity-covered as of SPEC-04 — see `docs/determinism.md` for
the full determinism + compile-key contract.

## Add a corpus document

1. Write it under the right group in `testdata/golden-corpus/` (original
   content, or license-noted).
2. Add its row to `testdata/golden-corpus/README.md`'s table.
3. Re-record fixtures and regen goldens (below), then commit with a
   "Golden changes" PR section.

## Record fixtures

Fixtures map canonicalized requests (key-sorted JSON, timestamps
sentinel-normalized) to responses. The committed baseline was recorded
against the scripted origin (`internal/parity/origin.go`); a maintainer
can re-record against a real vendor:

    SAGE_PARITY_FORCE=1 make record-fixtures                          # scripted origin
    SAGE_PARITY_FORCE=1 ORIGIN=https://api.openai.com KEY=<api key> [MODEL=<model>] make record-fixtures  # real vendor

CI never runs record mode. Record when prompts/pipeline legitimately
change and review fixtures like code.

## Authorize a golden change

Goldens change ONLY when a spec authorizes an output change:

    SAGE_PARITY_FORCE=1 make regen-goldens

Then bump `golden_format_version` in the affected golden if the schema
changed, and explain every diff category in the PR's "Golden changes"
section — CI fails the parity job when `testdata/golden/` or
`testdata/fixtures/` changes without that section.
