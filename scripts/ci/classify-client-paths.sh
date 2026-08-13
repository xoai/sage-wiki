#!/usr/bin/env bash
# classify-client-paths.sh — decide whether a set of changed paths is relevant
# to the client/example contracts (20260812-ci-quality-system Task 11).
#
# Usage:
#   classify-client-paths.sh            read changed paths from stdin (one per
#                                       line, e.g. `git diff --name-only`);
#                                       exit 0 = relevant, 1 = not relevant,
#                                       2 = usage/parse error (fail closed).
#   classify-client-paths.sh --self-test  run the contract cases; exit 0/1.
#
# Fail-closed contract: an API-contract change (api/openapi.yaml, internal/api,
# internal/mcp, cmd) MUST classify relevant — a classifier that misses it would
# silently skip the client contract tests on exactly the changes that break
# clients. Ambiguous or unreadable input exits 2, never "not relevant".
set -euo pipefail

is_relevant() { # one path -> return 0 if client/example-relevant
	case "$1" in
	clients/* | examples/*) return 0 ;;
	scripts/p4-fixture-server.sh) return 0 ;;
	api/openapi.yaml) return 0 ;;
	internal/* | cmd/*) return 0 ;;
	.github/workflows/clients.yml | .github/workflows/ci-diagnostics.yml) return 0 ;;
	scripts/ci/classify-client-paths.sh) return 0 ;;
	*) return 1 ;;
	esac
}

classify() { # stdin paths -> exit 0 relevant / 1 not / 2 error
	local seen=0 relevant=1 line
	while IFS= read -r line || [[ -n "$line" ]]; do
		# trim surrounding whitespace; skip blank lines
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line%"${line##*[![:space:]]}"}"
		[[ -z "$line" ]] && continue
		seen=1
		if is_relevant "$line"; then
			relevant=0
		fi
	done
	if [[ "$seen" -eq 0 ]]; then
		# No paths at all is ambiguous (not "irrelevant") — fail closed.
		return 2
	fi
	return "$relevant"
}

self_test() {
	local fail=0
	t() { # name want_exit paths...
		local name="$1" want="$2"
		shift 2
		local got
		if printf '%s\n' "$@" | classify; then
			got=0
		else
			got=$?
		fi
		if [[ "$got" != "$want" ]]; then
			echo "SELF-TEST FAIL: $name (exit $got, want $want)" >&2
			fail=1
		else
			echo "ok: $name"
		fi
	}

	t "client source change" 0 "clients/python/src/sagewiki/__init__.py"
	t "typescript client change" 0 "clients/typescript/src/index.ts"
	t "example change" 0 "examples/langgraph/main.py"
	t "api contract change must not be missed" 0 "api/openapi.yaml"
	t "server api change must not be missed" 0 "internal/api/handlers_read.go"
	t "mcp tool change must not be missed" 0 "internal/mcp/tools_write.go"
	t "cli change must not be missed" 0 "cmd/sage-wiki/main.go"
	t "fixture server change" 0 "scripts/p4-fixture-server.sh"
	t "mixed relevant and irrelevant" 0 "README.md" "clients/python/pyproject.toml"
	t "docs only is not relevant" 1 "README.md" "docs/security.md"
	t "unrelated go module file is not relevant" 1 "go.mod" ".github/workflows/docker.yml"
	t "empty input fails closed" 2 ""

	[[ "$fail" -eq 0 ]] && echo "SELF-TEST PASS" || exit 1
}

if [[ "${1:-}" == "--self-test" ]]; then
	self_test
	exit $?
fi

classify
