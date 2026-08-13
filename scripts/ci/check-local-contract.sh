#!/usr/bin/env bash

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT"

printf '%s\n' 'local-contract: checker self-tests'
bash scripts/ci/check-format.sh --self-test
bash scripts/ci/check-modules.sh --self-test
bash scripts/ci/run-go-tests.sh --self-test
bash scripts/ci/classify-client-paths.sh --self-test

printf '%s\n' 'local-contract: format and module inputs'
bash scripts/ci/check-format.sh
bash scripts/ci/check-modules.sh

printf '%s\n' 'local-contract: responsibility manifests'
go test ./tools/civalidate -count=1
for wf in ci ci-shadow fuzz ci-diagnostics; do
	go run ./tools/civalidate \
		--standards ci/standards.yaml \
		--packages ci/package-ownership.yaml \
		--platforms ci/platform-contracts.yaml \
		--services ci/service-contracts.yaml \
		--fuzz-targets ci/fuzz-targets.yaml \
		--workflow ".github/workflows/$wf.yml" \
		--makefile Makefile
done

printf '%s\n' 'local-contract: determinism self-test and scan'
bash scripts/check-determinism.sh --self-test
bash scripts/check-determinism.sh

printf '%s\n' 'local-contract: generated, API, and skill drift'
go test ./tools/skillgen -run '^(TestRegenerateIdempotent|TestOutputMatchesCommitted)$' -count=1
go test ./internal/api -run '^TestDrift_' -count=1

printf '%s\n' 'local-contract: translation self-test and inventory'
bash scripts/check-readme-translations.sh --self-test
bash scripts/check-readme-translations.sh --verify-headers

# Mutation witness: the header inventory must fail closed when a translation
# file is absent (a missing locale is drift, not a silent pass).
printf '%s\n' 'local-contract: translation inventory drift mutation'
mt_tmp=$(mktemp -d)
trap 'rm -rf "$mt_tmp"' EXIT
mkdir -p "$mt_tmp/docs/translations"
for loc in fr ja ko ru vi; do
	printf 'translations: may-lag\n\n# README\n' > "$mt_tmp/docs/translations/README_${loc}.md"
done
if (cd "$mt_tmp" && bash "$ROOT/scripts/check-readme-translations.sh" --verify-headers) >/dev/null 2>&1; then
	printf 'check-local-contract: missing-translation mutation passed (must fail)\n' >&2
	exit 1
fi
printf '%s\n' 'ok: missing translation file turns the header inventory red'

printf '%s\n' 'local-contract: hosted-only omissions'
printf '%s\n' \
	'- Windows and macOS execution: hosted workflow artifacts' \
	'- PostgreSQL and MinIO contracts: hosted service logs' \
	'- Frontend dist: pinned node:22-alpine build and diff' \
	'- Random fuzz exploration: scheduled crasher artifacts' \
	'- Release/publication: exact-SHA proof and provenance'
printf '%s\n' 'check-local-contract: OK'
