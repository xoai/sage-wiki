# Design: P2-5 — Extractor fuzzing suite

**Status:** draft (first commit of PR per Phase-2 spec preamble)
**Spec:** `.sage/docs/sage-wiki-upgrade/06-spec-phase2-strategic.md` §P2-5
**Cycle:** `.sage/work/20260724-p2-5-extractor-fuzzing/`

## 1. Problem

Six parsers ingest untrusted files (docx/xlsx/pptx/epub/eml/pdf). P1-7
built decompression caps (ziplimits.go: per-entry + aggregate budgets,
exceeded-flag overflow signaling). Nothing adversarially proves the caps
hold or that the hand-rolled XML/PDF/email parsing is panic-free. Fuzzing
is the proof instrument.

## 2. Design decisions

### D1 — Path-based targets via temp files, no parser refactor

All six entry points take a filesystem path (zip.OpenReader / os.Open).
Fuzz targets write the input to `t.TempDir()` and call the parser
directly — the standard Go pattern for path-based code. A byte-based
parser API refactor is explicitly rejected: it changes production code
for test convenience with zero security value (the fuzz target exercises
the same code path either way).

### D2 — Assertions are security invariants, never error paths

Per memory (P1-7): the stdlib already bounds lying headers, so
implementation-defined error paths are the wrong thing to assert. Every
target asserts ONLY:
1. **No panic** — the parser returns an error or content, never crashes
   (recovered panics inside the parser count as failures; Go fuzzing
   catches these natively).
2. **No budget breach** — when the parse succeeds, the returned
   `SourceContent` size stays within the P1-7 budget envelope. The exact
   envelope: output text ≤ aggregate budget from ziplimits.go (the
   package-level cap — read at assertion time so cap changes don't break
   the target). Per-entry detail stays unit-test territory.
3. **Termination** — the call returns (fuzz's own timeout watches for
   pathological loops).

### D3 — Programmatic minimal seeds, no binary downloads

Seeds are built in code: a minimal valid zip with the expected XML
members for each office format (word/document.xml etc.), a minimal mbox
message, a minimal valid PDF (%PDF-1.4 with one text object), a minimal
epub (zip with OPF). Built once per target in helper functions
(`seedsDocx() [][]byte` etc.), `f.Add(...)`-ed, plus Go's testdata/fuzz
dir for discovered crashers (D5).

### D4 — Nightly CI job, separate from the PR path

ci.yml gains a `fuzz` job: `schedule: cron '17 3 * * *'` (nightly, off-peak
UTC) + `workflow_dispatch`. Linux only, `go test -run=Fuzz -fuzz=Fuzz
-fuzztime=60s ./internal/extract/` per target (six sequential invocations,
60s each — total ≤ ~8 min incl. build). NOT in the PR-required job set
(separate job; PRs never block on it). Failure = the job fails loudly in
the nightly run; artifacts upload the fuzz cache dir for crasher
harvesting.

### D5 — Crashers as regression seeds

Go's standard layout: crashers land in
`internal/extract/testdata/fuzz/FuzzExtractDocx/<hash>` and run as
ordinary unit tests on every `go test` — zero harness work beyond
committing the files (f.Add forms for non-file seeds, the testdata dir
for discovered ones). Any crasher found is FIXED first, then checked in.

### D6 — Caps test-memory discipline

Per memory: big zero-buffer fixtures are cheap to build; package-var caps
lowered in tests need t.Cleanup restore and no t.Parallel. Fuzz targets
assert against the CURRENT package caps (read at call time), never
hard-code them, and never run parallel (they're fuzz targets — Go
serializes them anyway).

## 3. Non-goals

LLM-surface fuzzing, storage/MCP fuzzing, continuous-fuzz infra,
byte-API refactor, corpus downloads, code-parser (internal/extract/
parsers) targets, PDF third-party forking.

## 4. Test strategy

- Per-target smoke: each fuzz target runs as a plain test (seeds only,
  `-run=FuzzExtractX`) green.
- 30s local fuzz per target during the cycle: findings fixed or pinned.
- CI YAML validated (the job parses; triggers correct).
- Budget-invariant assertion present in every target (grep-verified).
- Full suite + `CGO_ENABLED=0 go build ./...` green.
