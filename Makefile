# Local mirror of the CI quality gate (.github/workflows/ci.yml). Run `make ci`
# on a feature branch to reproduce the checks CI *gates* on.
#
# CGO policy matches CI: build/vet stay CGO_ENABLED=0 (the release binary is
# pure-Go, modernc.org/sqlite); `test` uses CGO_ENABLED=1 because the race
# detector hard-requires cgo. Enabling cgo for tests pulls in no cgo module deps.

GO ?= go
GOLANGCI_LINT_VERSION ?= v2.12.2
# Base branch for the new-issues lint filter (mirrors CI's --new-from-merge-base).
LINT_BASE ?= main

.PHONY: build build-webui vet test lint lint-new vuln tidy ci

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

# The CI *gates* only (build/vet/test/new-issue lint). `vuln` is advisory in CI,
# so it is deliberately excluded here — run `make vuln` separately.
ci: build build-webui vet test lint-new
