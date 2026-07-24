# Design: P2-5 — Extractor fuzzing suite

**Status:** draft, review iteration 2 (first commit of PR per Phase-2 spec preamble)

> Iteration log: i1 found 0C/4M/4S/1cos — termination claim was mechanism-free,
> budget invariant undefined for eml/pdf, schedule-trigger semantics misread
> (whole workflow would run nightly), seed realism (extractXMLText captures
> ONLY `<t>` elements — seeds must enumerate exact members + `<t>` or the
> success path never fires; also surfaced a latent epub-extraction issue,
> logged as a follow-up, not fixed here). All folded in below.
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

### D2 — Per-target invariant sets, never error paths (i1 precision)

Per memory (P1-7): the stdlib already bounds lying headers, so
implementation-defined error paths are the wrong thing to assert. Only
UNRECOVERED panics count (a parser recovering its own panic returns
normally and is invisible to fuzzing — accepted). Assertion sets:

| Target | Invariants |
|---|---|
| docx / xlsx / pptx / epub (zip) | no panic; on success, `len(sc.Text) ≤ maxZipTotalBytes + slack` — slack = 64 KB for chapter/sheet header lines and filenames the writer adds on top of charged bytes (i1: strict ≤ would false-fail on headers) |
| eml | no panic; on success, `len(sc.Text) ≤ len(input)` — MIME body text is a subset of the input; eml has no P1-7 cap (i1: the budget invariant is vacuous here) |
| pdf (extractPDFGo only) | no panic; on success, `len(sc.Text) ≤ maxZipTotalBytes` as a sanity ceiling (pdf has no P1-7 cap; the pure-Go path can inflate compressed streams) |

Caps are read from the package vars at assertion time (maxZipTotalBytes /
maxZipEntryBytes exist as package-level vars in ziplimits.go — never
hard-coded in tests).

**Termination (i1 correction):** Go fuzzing has NO per-case timeout —
dropped as an invariant. Hangs are bounded at the PROCESS level:
`-fuzztime` bounds each target's run and the CI job carries a hard
timeout (60s×6 + margin), so a pathological loop surfaces as a visibly
failed nightly job, never as a crasher file. No goroutine watchdogs
(leaks at fuzz scale).

### D3 — Programmatic minimal seeds that REACH the success path (i1)

extractXMLText captures CharData ONLY inside elements literally named
`t` (namespace-stripped local name) — seeds without `<t>` always yield
empty content and never exercise the budget path. Member-level seed spec
(verified against the parsers):
- **docx**: `word/document.xml` containing `<w:t>hello</w:t>`
- **xlsx**: `xl/sharedStrings.xml` (`<si><t>shared</t></si>`) +
  `xl/worksheets/sheet1.xml` (`<t>cell</t>`)
- **pptx**: `ppt/slides/slide1.xml` (`<a:t>slide</a:t>`)
- **epub**: `content.xhtml` containing `<t>chapter</t>` (epub.go filters
  .xhtml/.html/.htm members; OPF is ignored entirely)
- **eml**: minimal RFC822 message (headers + plain body)
- **pdf**: minimal %PDF-1.4 with one uncompressed text object
Built programmatically per target, `f.Add`-ed, plus testdata/fuzz for
discovered crashers (D5). **Latent issue logged (follow-up, not fixed
here):** real-world XHTML epubs have no `<t>` elements and extract
empty — recorded in the cycle decisions; realistic-XHTML fuzz inputs
will surface it as empty-but-successful parses.

### D4 — Separate fuzz workflow file (i1 trigger-semantics correction)

`schedule:` is workflow-LEVEL — adding cron to ci.yml would run the
ENTIRE matrix (3-OS build/test, lint, vuln, frontend) nightly. Instead:
`.github/workflows/fuzz.yml` with `on: { schedule: [cron: '17 3 * * *'],
workflow_dispatch: {} }`. One linux job, `timeout-minutes: 15`, running
`go test -run=NONE -fuzz=FuzzExtractDocx -fuzztime=60s ./internal/extract/`
(and 5 more targets sequentially — one shared invocation would run all
targets but explicit per-target logs make failures attributable at a
glance; the rebuild overhead is one compile). Never on the PR path.
**PDF target is `extractPDFGo`** (unexported, same package): the public
extractPDF shells out to pdftotext when poppler is on PATH (fuzzing an
external binary at exec-per-case cost — wrong surface).

### D5 — Crashers as regression seeds, with an owned harvesting loop (i1)

Go's standard layout: crashers land in
`internal/extract/testdata/fuzz/FuzzExtractDocx/<hash>` on the machine
that found them and replay as ordinary unit tests on every `go test`.
The CI loop is explicit: the nightly job uploads the package's
`testdata/fuzz/` as an artifact on failure; the harvesting step (a human
or agent downloads the artifact, fixes the bug, commits the seed file) is
a NAMED manual step in the cycle's acceptance checklist — "fixed and
added as seeds" depends on it, and the design does not pretend it is
automatic.

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
- Budget-invariant assertion present per target per the D2 table
  (grep-verified); per-entry detail stays in unit tests (the workflow
  brief's "per-entry and aggregate" phrasing is superseded by the D2
  table).
- Full suite + `CGO_ENABLED=0 go build ./...` green.
