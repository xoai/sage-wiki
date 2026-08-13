#!/usr/bin/env bash

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

check_format() {
	local root=$1
	local -a files=()
	while IFS= read -r -d '' file; do
		files+=("$file")
	done < <(git -C "$root" ls-files -z -- '*.go')
	if ((${#files[@]} == 0)); then
		printf 'check-format: tracked Go source inventory is empty\n' >&2
		return 1
	fi

	local output
	if ! output=$(cd "$root" && gofmt -l -- "${files[@]}"); then
		printf 'check-format: gofmt failed\n' >&2
		return 1
	fi
	if [[ -n "$output" ]]; then
		printf 'check-format: unformatted tracked Go source:\n%s\n' "$output" >&2
		while IFS= read -r file; do
			[[ -n "$file" ]] && printf '::error file=%s::run gofmt -w %s\n' "$file" "$file" >&2
		done <<< "$output"
		return 1
	fi
	printf 'check-format: OK (%d tracked Go files)\n' "${#files[@]}"
}

self_test() {
	local temp
	temp=$(mktemp -d)
	trap 'rm -rf "$temp"' RETURN
	git -C "$temp" init -q
	git -C "$temp" config user.email test@example.com
	git -C "$temp" config user.name test

	if check_format "$temp" >/dev/null 2>&1; then
		printf 'check-format self-test: empty inventory passed\n' >&2
		return 1
	fi

	printf 'package fixture\n\nfunc ok() {}\n' > "$temp/formatted.go"
	git -C "$temp" add formatted.go
	if ! check_format "$temp" >/dev/null; then
		printf 'check-format self-test: formatted source failed\n' >&2
		return 1
	fi

	printf 'package fixture\nfunc bad( ){ }\n' > "$temp/unformatted.go"
	git -C "$temp" add unformatted.go
	if check_format "$temp" >/dev/null 2>&1; then
		printf 'check-format self-test: unformatted source passed\n' >&2
		return 1
	fi
	printf 'check-format self-test: OK\n'
}

if [[ ${1:-} == "--self-test" ]]; then
	self_test
	exit $?
fi
if (($#)); then
	printf 'usage: %s [--self-test]\n' "$0" >&2
	exit 2
fi
check_format "$ROOT"
