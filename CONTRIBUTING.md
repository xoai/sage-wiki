# Contributing to sage-wiki

**Before opening a PR:** run `make ci` on your feature branch — the accurate
local fast gate. It runs, in order: canonical formatting over all tracked Go
source (`scripts/ci/check-format.sh`), module tidy-drift and content
verification (`scripts/ci/check-modules.sh`, worktree-preserving), pure-Go
and webui builds, vet, new-issue lint, responsibility-manifest validation
(`tools/civalidate`: exact package partition, aggregate membership, Make
targets, determinism roles, platform inventory), the determinism tripwire
with its self-test, generated/API/skill drift checks, translation self-test
and header inventory, and the ordinary (non-race) test suite.
On `main` itself the *local* translation-drift range is empty (CI's push path
checks `before..after` and is not), so run `make translations` from your
branch for the range-based check.

**`make ci-race`** is the canonical local race contract: `-race` over every
manifest-owned package (`make test` is the legacy race alias). Run it before
pushing compiler/concurrency work; `make ci` deliberately stays non-race for
speed.

**Local `make ci` is mandatory but not a substitute for hosted CI.** It
prints, on success, exactly what it does NOT cover — hosted-only evidence:
Windows/macOS execution, PostgreSQL/MinIO service contracts, the
pinned-container frontend build, scheduled fuzz exploration, and exact-SHA
publication proof. The hosted `CI required` check-run on the latest PR SHA is
the merge gate: a green `make ci` does not replace a green hosted run, and a
hosted result that predates your last push does not count. Today that gate
is maintainer policy — a PR is not mergeable without `CI required` success
on the HEAD SHA — and it becomes mechanical once branch protection requires
the check on `main`.

## CI responsibility and ownership states

Quality responsibility is recorded in machine-readable manifests under
`ci/` (`standards.yaml`, `package-ownership.yaml`,
`platform-contracts.yaml`), validated fail-closed by `tools/civalidate`
against the live tree. Two ownership states matter when reading CI:

- **Required now (`required-requalifying`).** The current required jobs —
  build, parity, go-test, fuzz-short, skill-drift, postgres, minio, lint,
  frontend, translations — stay in the `CI required` aggregate and keep
  merge authority while they re-qualify under the shadow protocol.
- **Candidate (advisory).** Target witnesses (preflight checks, focused OS
  contracts, service-contract shards) run advisory only. The CI workflow's
  `Responsibility validation (advisory)` job may turn red — a validator or
  parser failure stays visible — but it is deliberately outside the
  `CI required` aggregate and cannot block a merge. Candidates earn
  promotion only through a recorded qualification window (at least 20
  relevant executions over seven days, zero unexplained divergence) and
  explicit maintainer approval.

If the advisory job is red on your PR, treat it as a real signal — fix the
underlying manifest/validation failure — but it does not gate merging while
it remains advisory.

## Repository layout

Selected entries (illustrative, not exhaustive):

```
├── cmd/sage-wiki/        # CLI entrypoint + command wiring
├── internal/             # the core (~30 packages): compiler, llm, storage,
│                         #   memory, vectors, search, graph, ontology, mcp,
│                         #   trust, api (/v1 REST), web, tui, linter, …
├── pkg/sagewiki/         # public Go module for embedding (in-process MCP)
├── clients/              # SDKs: python/ + typescript/
├── tools/skillgen/       # agent-skill generator (skills/ is generated output)
├── tools/civalidate/     # fail-closed CI responsibility validator
├── tools/testsummary/    # go test -json summarizer (annotations, exit-preserving)
├── ci/                   # responsibility manifests (standards, ownership, contracts)
├── skills/               # generated agent skills — never hand-edit; CI drift-checked
├── examples/             # CI-exercised framework examples (langgraph, vercel-ai-sdk)
├── eval/                 # benchmarks (LOCOMO, LongMemEval, BEAM)
├── api/openapi.yaml      # the /v1 REST contract (drift-checked against tools+routes)
├── web/                  # Preact web UI source (embedded via -tags webui)
├── docs/                 # guides/ + translations/ (six README locales)
├── assets/               # README images
└── scripts/              # CI/dev shell tools
```

## Translations

`README.md` and its six translations (`docs/translations/README_{fr,ja,ko,ru,vi,zh}.md`)
move together. A change range that touches `README.md` without any
`docs/translations/README_*.md` fails CI's Translation drift job (MAINT-05) —
`make ci` runs the same check locally. If the change genuinely should not be
translated yet, add `translations: lag-ok` to a commit message in the range
to document the debt.

**Maintainers merging external PRs:** GitHub holds CI for first-time
contributors at `action_required` — checks must have *run and passed*, not
merely be absent. Click "Approve and run workflows" on fork PRs, and treat a
PR showing zero checks as unverified regardless of local runs. When reworking
CI workflow code, keep the required context valid: once branch protection
requires `CI required` on `main`, relax or remove that required status check
before reverting or renaming the workflow that emits the check-run — a
required context that no workflow produces blocks every merge.

## Adding a file format parser

### Go (built-in)

1. Add a new case in `internal/extract/extract.go` matching the file extension
2. Implement the extraction function returning plain text content
3. Add tests in `internal/extract/extract_test.go`

### External (subprocess)

1. Write a parser script that reads file content from stdin and writes plain text to stdout
2. Create `parsers/parser.yaml` in your project or pack with the extension mapping:
   ```yaml
   parsers:
     - extensions: [".docx"]
       command: python3
       args: ["docx_parser.py"]
       timeout: 30s
   ```
3. Place the script in `parsers/` and ensure it's executable (relative paths in `command` and `args` are resolved against `parsers/`)
4. Enable external parsers in config: `parsers: { external: true }`

External parsers run with timeout enforcement (30s default, 120s max) and
environment stripping (only PATH, HOME, LANG reach the subprocess). They
require double opt-in: `parsers.external: true` to load definitions and
`parsers.trust_external: true` to acknowledge unsandboxed execution; packs
with parsers additionally need `pack apply --enable-parsers`. Built-in
extractors are hardened with decompression caps (per-entry and aggregate
limits against zip bombs) and covered by a nightly fuzzing job
([.github/workflows/fuzz.yml](.github/workflows/fuzz.yml)) that feeds
malformed docx/xlsx/pptx/epub/eml/pdf inputs and checks the caps hold.

## Fuzzing

Native Go fuzz targets guard the parsing and hardening surfaces. Two tiers run
in CI, both driven by the machine-readable inventory in
[`ci/fuzz-targets.yaml`](ci/fuzz-targets.yaml) (validated fail-closed against
the source tree — a new target without an inventory entry, or a stale entry,
turns the nightly job red):

- **PR-gated short pass** (`fuzz-short` job in
  [.github/workflows/ci.yml](.github/workflows/ci.yml)): the 8 hardening
  targets (`FuzzFrontmatter` ×5 packages, `FuzzWikilink`,
  `FuzzAliasNormalize`, `FuzzCanonical`) for 30s each.
- **Nightly exploration** ([.github/workflows/fuzz.yml](.github/workflows/fuzz.yml)):
  every target in the inventory — the 8 hardening targets plus the 6 extractor
  format targets (`FuzzExtract{Docx,Xlsx,Pptx,Epub,Email,PdfGo}`), which do
  **not** run on PRs.

Committed seed corpora run deterministically as ordinary package tests on
every PR; only the time-bounded random exploration above is scheduled.

Run a target locally (pick one target per invocation — Go's `-fuzz` errors if
a pattern matches more than one):

```sh
go test -run=NONE -fuzz=FuzzFrontmatter -fuzztime=30s ./internal/extract/
go test -run=NONE -fuzz=FuzzWikilink    -fuzztime=30s ./internal/compiler/
```

The current targets:

| Target | Package | Surface |
|--------|---------|---------|
| `FuzzExtract{Docx,Xlsx,Pptx,Epub,Email,PdfGo}` | `internal/extract` | extractor decompression caps |
| `FuzzFrontmatter` | `internal/extract`, `internal/web`, `internal/ontology`, `internal/wiki`, `internal/compiler` | the five pure-string frontmatter sites (one target per owning package) |
| `FuzzWikilink` | `internal/compiler` | wikilink matching, sanitization, broken-link strip |
| `FuzzAliasNormalize` | `internal/compiler` | name normalization + alias map |
| `FuzzCanonical` | `internal/compiler` | canonical frontmatter/JSON determinism |

Assertions are security invariants only — no panic, no unbounded growth,
deterministic output. Errors are accepted, never asserted.

If a run finds a crash, Go writes the failing input under
`<package>/testdata/fuzz/<Target>/`. **Commit that crasher file** with your
fix so the corpus always reproduces it:

```sh
git add internal/compiler/testdata/fuzz/FuzzWikilink/   # example
```

## Regenerating the web UI dist

The committed `internal/web/dist` must byte-match a `node:22-alpine` build
(CI enforces this with a hard-fail drift check). After changing anything
under `web/`, regenerate inside the pinned environment and commit the
result:

```sh
docker run --rm -v "$PWD:/src" -w /src/web node:22-alpine sh -c "npm ci && npm run build"
```


## Creating a pack

### Quick start

```bash
sage-wiki pack create my-pack
cd my-pack
# edit pack.yaml, add prompts, skills, samples
sage-wiki pack validate
```

### Pack directory structure

```
my-pack/
├── pack.yaml              # required — manifest
├── prompts/               # optional — prompt templates
│   └── summarize.txt
├── skills/                # optional — skill template files
├── parsers/               # optional — external parser scripts
│   ├── parser.yaml        # extension mappings
│   └── convert.py         # parser script
├── samples/               # optional — example source files
│   └── example.md
└── README.md              # optional — documentation
```

### Testing your pack

```bash
# validate schema and file references
sage-wiki pack validate ./my-pack

# install locally and apply to a test project
sage-wiki pack install ./my-pack
sage-wiki pack apply my-pack --mode merge

# verify config and ontology changes
sage-wiki status
```

### Pack command reference

```bash
sage-wiki pack install <name|url>     # install (bundled name or Git URL)
sage-wiki pack apply <name> [--mode merge|replace]   # apply to the project
sage-wiki pack remove <name>          # remove a pack from the project
sage-wiki pack list                   # list applied, cached, and bundled packs
sage-wiki pack search <query>         # search the pack registry
sage-wiki pack update [name]          # update installed packs to latest versions
sage-wiki pack info <name>            # show details about a pack
sage-wiki pack create <name>          # scaffold a new pack directory
sage-wiki pack validate [path]        # validate a pack's schema and files
sage-wiki pack conflicts              # show multi-pack file overlaps
```

Packs are composable — apply multiple packs and their ontology types are
union-merged. Conflicts (overlapping prompt files) are reported; use
`sage-wiki pack conflicts` to inspect.

### Submitting to the registry

1. Fork the [sage-wiki-packs](https://github.com/xoai/sage-wiki-packs) repository
2. Add your pack directory under `packs/`
3. Add an entry to `index.yaml`:
   ```yaml
   - name: my-pack
     version: 1.0.0
     description: Short description of what this pack does
     tier: community
     tags: [domain, keywords]
   ```
4. Run `sage-wiki pack validate packs/my-pack` to verify
5. Submit a pull request — CI validates the pack automatically

## Pack schema reference

### pack.yaml fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Kebab-case identifier (`^[a-z][a-z0-9-]*$`) |
| `version` | string | yes | Semantic version (e.g., `1.0.0`) |
| `description` | string | yes | One-line description |
| `author` | string | yes | Author name or organization |
| `license` | string | no | License identifier (e.g., `MIT`) |
| `min_version` | string | no | Minimum sage-wiki version required |
| `tags` | string[] | no | Discovery tags |
| `homepage` | string | no | URL to project homepage |
| `config` | map | no | Config overlay (merged into project config.yaml) |
| `ontology.entity_types` | object[] | no | Entity type definitions |
| `ontology.relation_types` | object[] | no | Relation type definitions |
| `article_fields` | string[] | no | Custom article metadata fields |
| `prompts` | string[] | no | Prompt template filenames in `prompts/` |
| `skills` | string[] | no | Skill template filenames in `skills/` |
| `parsers` | string[] | no | Parser script filenames in `parsers/` |
| `samples` | string[] | no | Sample source filenames in `samples/` |

### Config overlay

Only these top-level config keys are allowed in pack config overlays:

- `compiler` — compilation settings (default_tier, etc.)
- `search` — search and retrieval settings
- `linting` — linting rules
- `ontology` — ontology configuration
- `trust` — output trust settings
- `type_signals` — type signal configuration
- `ignore` — ignore patterns

Keys like `api`, `embed`, `models`, `parsers`, `serve`, `vault`, `sources`,
`output`, and `project` are stripped for security.

### Entity and relation types

```yaml
ontology:
  entity_types:
    - name: finding
      description: A research finding or result
  relation_types:
    - name: cites
      synonyms: [references, builds_on]
      valid_sources: [article]  # optional
      valid_targets: [article]  # optional
```

### Apply modes

- **merge** (default) — fill-only: pack values apply only where the project has no value. Existing files are skipped as conflicts.
- **replace** — overwrites existing values and files.
