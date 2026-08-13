#!/usr/bin/env bash

set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

usage() {
	printf 'usage: %s --standard ID --raw FILE -- COMMAND [ARG...]\n' "$0" >&2
}

quote_command() {
	local quoted="" arg
	for arg in "$@"; do
		printf -v arg '%q' "$arg"
		quoted+="${quoted:+ }$arg"
	done
	printf '%s' "$quoted"
}

run_command() {
	local standard=$1 raw=$2
	shift 2
	local command summary_path
	command=$(quote_command "$@")
	summary_path=${GITHUB_STEP_SUMMARY:-}

	mkdir -p "$(dirname "$raw")"
	local -a summary_args=(--input - --standard "$standard" --command "$command" --passthrough)
	if [[ -n "$summary_path" ]]; then
		summary_args+=(--summary "$summary_path")
	fi
	"$@" | tee "$raw" | (
		cd "$ROOT"
		summary_status=0
		go run ./tools/testsummary "${summary_args[@]}" || summary_status=$?
		if (( summary_status != 0 )); then
			# A fail-fast parser must not close the pipe and replace the source
			# command's status with SIGPIPE.
			cat
		fi
		exit "$summary_status"
	)
	local -a pipeline_status=("${PIPESTATUS[@]}")
	local source_status=${pipeline_status[0]}
	local tee_status=${pipeline_status[1]}
	local summary_status=${pipeline_status[2]}

	if (( source_status != 0 )); then
		return "$source_status"
	fi
	if (( tee_status != 0 )); then
		return "$tee_status"
	fi
	return "$summary_status"
}

self_test() {
	local temp
	temp=$(mktemp -d)
	trap 'rm -rf "$temp"' RETURN
	local fixture="$ROOT/tools/testsummary/testdata"

	set +e
	run_command test-standard "$temp/source-failure.json" bash -c 'while IFS= read -r line; do printf "%s\n" "$line"; done < "$1"; exit 17' _ "$fixture/success.json" >/dev/null 2>&1
	local status=$?
	set -e
	if (( status != 17 )) || ! cmp -s "$fixture/success.json" "$temp/source-failure.json"; then
		printf 'run-go-tests self-test: source status/raw preservation failed (status=%d)\n' "$status" >&2
		return 1
	fi

	set +e
	local expected_early="$temp/expected-early-parser-failure.json"
	{
		printf '{not-json}\n'
		for ((i=0; i<20000; i++)); do printf 'padding-%05d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n' "$i"; done
	} > "$expected_early"
	run_command test-standard "$temp/early-parser-failure.json" bash -c '
		while IFS= read -r line; do printf "%s\n" "$line"; done < "$1"
		exit 23
	' _ "$expected_early" >/dev/null 2>&1
	status=$?
	set -e
	if (( status != 23 )) || ! cmp -s "$expected_early" "$temp/early-parser-failure.json"; then
		printf 'run-go-tests self-test: early parser failure lost source status or raw evidence (status=%d)\n' "$status" >&2
		return 1
	fi

	set +e
	run_command test-standard "$temp/summary-failure.json" bash -c 'while IFS= read -r line; do printf "%s\n" "$line"; done < "$1"' _ "$fixture/test-failure.json" >/dev/null 2>&1
	status=$?
	set -e
	if (( status != 1 )); then
		printf 'run-go-tests self-test: summary failure status was %d, want 1\n' "$status" >&2
		return 1
	fi

	for name in malformed empty; do
		set +e
		run_command test-standard "$temp/$name.json" bash -c 'while IFS= read -r line; do printf "%s\n" "$line"; done < "$1"' _ "$fixture/$name.json" >/dev/null 2>&1
		status=$?
		set -e
		if (( status == 0 )); then
			printf 'run-go-tests self-test: %s evidence passed\n' "$name" >&2
			return 1
		fi
	done

	run_command test-standard "$temp/success.json" bash -c 'while IFS= read -r line; do printf "%s\n" "$line"; done < "$1"' _ "$fixture/success.json" >/dev/null
	printf 'run-go-tests self-test: OK\n'
}

if [[ ${1:-} == "--self-test" ]]; then
	self_test
	exit $?
fi

standard=""
raw=""
while (($#)); do
	case "$1" in
		--standard)
			standard=${2:-}
			shift 2
			;;
		--raw)
			raw=${2:-}
			shift 2
			;;
		--)
			shift
			break
			;;
		*)
			usage
			exit 2
			;;
	esac
done

if [[ -z "$standard" || -z "$raw" || $# -eq 0 ]]; then
	usage
	exit 2
fi

run_command "$standard" "$raw" "$@"
