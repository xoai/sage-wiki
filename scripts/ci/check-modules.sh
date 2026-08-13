#!/usr/bin/env bash

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
GO=${GO:-go}

check_modules() {
	local root=$1
	local tidy_output
	if ! tidy_output=$(cd "$root" && "$GO" mod tidy -diff 2>&1); then
		printf 'check-modules: go.mod/go.sum require tidy changes:\n%s\n' "$tidy_output" >&2
		return 1
	fi
	if ! (cd "$root" && "$GO" mod verify); then
		printf 'check-modules: module content verification failed\n' >&2
		return 1
	fi
	local packages
	if ! packages=$(cd "$root" && "$GO" list ./...); then
		printf 'check-modules: package discovery failed\n' >&2
		return 1
	fi
	if [[ -z "${packages//[[:space:]]/}" ]]; then
		printf 'check-modules: Go package inventory is empty\n' >&2
		return 1
	fi
	printf 'check-modules: OK (%d packages)\n' "$(wc -l <<< "$packages")"
}

self_test() {
	local temp fake_go
	temp=$(mktemp -d)
	trap 'rm -rf "$temp"' RETURN
	printf 'module example.com/fixture\n\ngo 1.26\n' > "$temp/go.mod"
	fake_go="$temp/fake-go"
	cat > "$fake_go" <<'EOF'
#!/usr/bin/env bash
set -eu
case "${MODULE_TEST_MODE:-pass}:$*" in
	tidy-drift:"mod tidy -diff") printf '%s\n' '--- go.mod' '+++ go.mod'; exit 1 ;;
	verify-fail:"mod verify") printf '%s\n' 'missing module content' >&2; exit 1 ;;
	empty:"list ./...") exit 0 ;;
	*:"mod tidy -diff") exit 0 ;;
	*:"mod verify") printf '%s\n' 'all modules verified'; exit 0 ;;
	*:"list ./...") printf '%s\n' 'example.com/fixture'; exit 0 ;;
	*) exit 2 ;;
esac
EOF
	chmod +x "$fake_go"
	local real_go=$GO mode
	GO=$fake_go
	if ! MODULE_TEST_MODE=pass check_modules "$temp" >/dev/null; then
		printf 'check-modules self-test: valid module failed\n' >&2
		GO=$real_go
		return 1
	fi
	for mode in tidy-drift verify-fail empty; do
		if MODULE_TEST_MODE=$mode check_modules "$temp" >/dev/null 2>&1; then
			printf 'check-modules self-test: %s mutation passed\n' "$mode" >&2
			GO=$real_go
			return 1
		fi
	done
	GO=$real_go
	printf 'check-modules self-test: OK\n'
}

if [[ ${1:-} == "--self-test" ]]; then
	self_test
	exit $?
fi
if (($#)); then
	printf 'usage: %s [--self-test]\n' "$0" >&2
	exit 2
fi
check_modules "$ROOT"
