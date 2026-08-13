# Local mirror of the CI quality gate (.github/workflows/ci.yml). Run `make ci`
# on a feature branch to reproduce the checks CI *gates* on.
#
# CGO policy matches CI: build/vet stay CGO_ENABLED=0 (the release binary is
# pure-Go, modernc.org/sqlite); race targets use CGO_ENABLED=1 because the race
# detector hard-requires cgo. Enabling cgo for tests pulls in no cgo module deps.
#
# Honesty contract (20260812-ci-quality-system Task 7): `make ci` is the fast
# local gate and prints what it does NOT cover. Hosted-only evidence — OS
# execution, PostgreSQL/MinIO services, pinned-container frontend, scheduled
# fuzz exploration, and exact-SHA publication proof — is never claimed here.

GO ?= go
GOLANGCI_LINT_VERSION ?= v2.12.2
# Base branch for the new-issues lint filter (mirrors CI's --new-from-merge-base)
# and the translation-drift range. Falls back to origin/main when no local main
# exists (detached worktrees); shallow clones need full history for merge-base.
BASE ?= main
LINT_BASE ?= $(BASE)

.PHONY: build build-webui vet test test-norace lint lint-new vuln tidy \
        format-check modules-check responsibility-check determinism-check \
        generated-check translations translations-self-test translations-headers \
        ci ci-race record-fixtures regen-goldens parity

build:
	CGO_ENABLED=0 $(GO) build ./...

build-webui:
	CGO_ENABLED=0 $(GO) build -tags webui ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

# Full race suite (legacy name kept for contributors and manifest references).
test:
	CGO_ENABLED=1 $(GO) test -race ./...

# Ordinary (non-race) suite — the `make ci` test leg. Environment-gated
# service tests (TEST_DATABASE_URL, SAGE_TEST_MINIO) keep their local skips.
test-norace:
	CGO_ENABLED=0 $(GO) test ./...

# Full report incl. the pre-existing backlog — for chipping away at it locally.
lint:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	bash scripts/check-determinism.sh

# Only NEW issues vs $(LINT_BASE) — exactly what CI gates. Green on unmodified main.
lint-new:
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --new-from-merge-base=$(LINT_BASE) ./...

# Advisory only — matches CI's continue-on-error vuln job. Currently flags Go
# stdlib advisories fixed only in a newer patch toolchain, so it is intentionally
# NOT part of the `ci` aggregate; run it on its own.
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy:
	$(GO) mod tidy

# --- Local-contract checks (fail-closed; each has a --self-test mutation suite) ---

# Canonical formatting over ALL tracked Go source (empty inventory is red).
format-check:
	bash scripts/ci/check-format.sh

# go.mod/go.sum tidy drift + module content verification (never mutates the
# worktree: tidy runs with -diff).
modules-check:
	bash scripts/ci/check-modules.sh

# Responsibility manifests: parser/validation suite plus the live fail-closed
# validator (exact package partition, aggregate membership, Make targets,
# determinism roles, platform inventory, shards, service contracts, fuzz
# inventory). Every workflow is validated so a witness job reference can
# never point at a job that does not exist: ci.yml (required aggregate),
# ci-shadow.yml (advisory candidates), fuzz.yml (scheduled exploration),
# and ci-diagnostics.yml (scheduled broad diagnostics).
responsibility-check:
	$(GO) test ./tools/civalidate -count=1
	$(GO) run ./tools/civalidate \
		--standards ci/standards.yaml \
		--packages ci/package-ownership.yaml \
		--platforms ci/platform-contracts.yaml \
		--services ci/service-contracts.yaml \
		--fuzz-targets ci/fuzz-targets.yaml \
		--workflow .github/workflows/ci.yml \
		--makefile Makefile
	$(GO) run ./tools/civalidate \
		--standards ci/standards.yaml \
		--packages ci/package-ownership.yaml \
		--platforms ci/platform-contracts.yaml \
		--services ci/service-contracts.yaml \
		--fuzz-targets ci/fuzz-targets.yaml \
		--workflow .github/workflows/ci-shadow.yml \
		--makefile Makefile
	$(GO) run ./tools/civalidate \
		--standards ci/standards.yaml \
		--packages ci/package-ownership.yaml \
		--platforms ci/platform-contracts.yaml \
		--services ci/service-contracts.yaml \
		--fuzz-targets ci/fuzz-targets.yaml \
		--workflow .github/workflows/fuzz.yml \
		--makefile Makefile
	$(GO) run ./tools/civalidate \
		--standards ci/standards.yaml \
		--packages ci/package-ownership.yaml \
		--platforms ci/platform-contracts.yaml \
		--services ci/service-contracts.yaml \
		--fuzz-targets ci/fuzz-targets.yaml \
		--workflow .github/workflows/ci-diagnostics.yml \
		--makefile Makefile

# Determinism tripwire: contract self-test first, then the live scan.
# Run sequentially — the self-test plants a temporary in-tree offender.
determinism-check:
	bash scripts/check-determinism.sh --self-test
	bash scripts/check-determinism.sh

# Generated/API/skill drift: byte-identical skill regeneration, committed-output
# match, and the OpenAPI/route/MCP agreement tests.
generated-check:
	$(GO) test ./tools/skillgen -run '^(TestRegenerateIdempotent|TestOutputMatchesCommitted)$$' -count=1
	$(GO) test ./internal/api -run '^TestDrift_' -count=1

# Translation drift (MAINT-05): README.md must move with at least one
# docs/translations/README_*.md translation, or a commit in the range carries
# `translations: lag-ok`. One shell per target: recipe lines would otherwise
# lose the computed vars between lines.
translations:
	@mb=$$(git merge-base "$$(git rev-parse --verify --quiet $(BASE) || git rev-parse --verify --quiet origin/main)" HEAD); 	COMMIT_MSGS="$$(git log --format=%B $$mb..HEAD)"; 	BASE="$(BASE)" HEAD=HEAD COMMIT_MSGS="$$COMMIT_MSGS" bash scripts/check-readme-translations.sh

# Contract test for the check itself (no repo state needed).
translations-self-test:
	bash scripts/check-readme-translations.sh --self-test

# The six committed translation files must carry their lag headers.
translations-headers:
	bash scripts/check-readme-translations.sh --verify-headers

# The accurate local fast gate. Deliberately excludes `vuln` (advisory in CI)
# and the range-based `translations` drift check (branch-context dependent;
# hosted CI computes the real range — run `make translations` on your branch).
# Ends with the hosted-only omissions so nobody mistakes this for full CI.
ci: format-check modules-check build build-webui vet lint-new \
    responsibility-check determinism-check generated-check \
    translations-self-test translations-headers test-norace
	@echo "make ci: local gate passed. NOT covered locally (hosted-only evidence):"
	@echo "  - Windows/macOS execution — hosted workflow artifacts"
	@echo "  - PostgreSQL/MinIO service contracts — hosted service logs"
	@echo "  - Frontend dist — pinned node:22-alpine build and diff"
	@echo "  - Random fuzz exploration — scheduled crasher artifacts"
	@echo "  - Release/publication — exact-SHA proof and provenance"

# The canonical local race contract: -race over every manifest-owned package.
# (`make test` is the legacy race alias, kept for contributors and manifest
# references; ci-race is the named contract target.)
ci-race:
	CGO_ENABLED=1 $(GO) test -race -timeout 15m ./...

# SPEC-09: record LLM fixtures via the scripted origin (default) or a real
# vendor (ORIGIN=https://... KEY=...). Maintainer action — CI never records.
record-fixtures:
	@test "$$SAGE_PARITY_FORCE" = "1" || { echo "refusing: set SAGE_PARITY_FORCE=1 (golden overwrite guard)"; exit 1; }
	ORIGIN="$(ORIGIN)" go test ./internal/parity/ -run TestRecordFixtures -count=1

# SPEC-09: regenerate goldens from the current code. Guarded; commit with a
# "Golden changes" PR section explaining every diff category.
regen-goldens:
	@test "$$SAGE_PARITY_FORCE" = "1" || { echo "refusing: set SAGE_PARITY_FORCE=1 (golden overwrite guard)"; exit 1; }
	go test ./internal/parity/ -run TestRegenGoldens -count=1

# SPEC-09: the parity suite (replay mode, offline).
parity:
	go test -count=1 ./internal/parity/
