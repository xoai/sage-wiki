# Local mirror of the CI quality gate (.github/workflows/ci.yml). Run `make ci`
# on a feature branch to reproduce the checks CI *gates* on.
#
# CGO policy matches CI: build/vet stay CGO_ENABLED=0 (the release binary is
# pure-Go, modernc.org/sqlite); `test` uses CGO_ENABLED=1 because the race
# detector hard-requires cgo. Enabling cgo for tests pulls in no cgo module deps.

GO ?= go
GOLANGCI_LINT_VERSION ?= v2.12.2
# Base branch for the new-issues lint filter (mirrors CI's --new-from-merge-base)
# and the translation-drift range. Falls back to origin/main when no local main
# exists (detached worktrees); shallow clones need full history for merge-base.
BASE ?= main
LINT_BASE ?= $(BASE)

.PHONY: build build-webui vet test lint lint-new vuln tidy ci translations translations-self-test

build:
	CGO_ENABLED=0 $(GO) build ./...

build-webui:
	CGO_ENABLED=0 $(GO) build -tags webui ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

test:
	CGO_ENABLED=1 $(GO) test -race ./...

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

# Translation drift (MAINT-05): README.md must move with at least one
# docs/translations/README_*.md translation, or a commit in the range carries
# `translations: lag-ok`. One shell per target: recipe lines would otherwise
# lose the computed vars between lines.
translations:
	@mb=$$(git merge-base "$$(git rev-parse --verify --quiet $(BASE) || git rev-parse --verify --quiet origin/main)" HEAD); 	COMMIT_MSGS="$$(git log --format=%B $$mb..HEAD)"; 	BASE="$(BASE)" HEAD=HEAD COMMIT_MSGS="$$COMMIT_MSGS" bash scripts/check-readme-translations.sh

# Contract test for the check itself (no repo state needed).
translations-self-test:
	bash scripts/check-readme-translations.sh --self-test

# The CI *gates* only (build/vet/test/new-issue lint/translations). `vuln` is
# advisory in CI, so it is deliberately excluded here — run `make vuln`
# separately. Run on your feature branch: on main itself the drift range is
# empty and `translations` is a no-op by design.
ci: build build-webui vet test lint-new translations

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
