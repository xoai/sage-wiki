#!/usr/bin/env bash
# check-readme-translations.sh — MAINT-05 / P2-7.
#
# Fails when a change range touches README.md but no README_*.md
# translation, unless a commit in the range carries the escape-hatch
# marker `translations: lag-ok`.
#
# Env:
#   BASE        — base ref/range start (required)
#   HEAD        — head ref (required)
#   COMMIT_MSGS — newline-separated commit messages for the range
#                 (optional when --self-test; computed by the CI step)
#
# --self-test runs the four contract cases locally and exits 0/1.
set -euo pipefail

check() { # BASE HEAD COMMIT_MSGS -> exit 1 on drift
	local base="$1" head="$2" msgs="$3"
	# Diff against the merge-base (three-dot semantics): two-dot would
	# blame the branch for README.md changes that landed on main after
	# the branch point (false block), and the caller computes COMMIT_MSGS
	# over the same range (a stale lag-ok from before the branch point
	# would false-pass otherwise).
	local mb changed
	mb="$(git merge-base "$base" "$head")"
	changed="$(git diff --name-only "$mb" "$head")"
	if ! grep -qx "README.md" <<< "$changed"; then
		return 0 # English README untouched — nothing to check
	fi
	if grep -qE '^docs/translations/README_[a-z]{2}\.md$' <<< "$changed"; then
		return 0 # a translation moved with it
	fi
	if grep -qF "translations: lag-ok" <<< "$msgs"; then
		return 0 # documented debt
	fi
	echo "::error::README.md changed without a translation update." >&2
	echo "Update a docs/translations/README_*.md or add 'translations: lag-ok' to a commit message." >&2
	return 1
}

if [[ "${1:-}" == "--self-test" ]]; then
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	cd "$tmp"
	git init -q . && git config user.email t@t && git config user.name t
	echo "x" > README.md && git add . && git commit -qm init

	fail=0
	t() { # name BASE HEAD MSGS want_exit
		local name="$1" base="$2" head="$3" msgs="$4" want="$5"
		if check "$base" "$head" "$msgs" >/dev/null 2>&1; then got=0; else got=1; fi
		if [[ "$got" != "$want" ]]; then
			echo "SELF-TEST FAIL: $name (exit $got, want $want)" >&2
			fail=1
		else
			echo "ok: $name"
		fi
	}

	# case 1: no README change → pass
	echo y > other.txt && git add . && git commit -qm "other"
	t "no readme change" HEAD~1 HEAD "" 0
	# case 2: README-only change → fail
	echo z >> README.md && git add . && git commit -qm "docs: english only"
	t "readme only" HEAD~1 HEAD "docs: english only" 1
	# case 3: README + translation in the SAME commit → pass
	echo t2 >> README.md && mkdir -p docs/translations && echo t > docs/translations/README_fr.md && git add . && git commit -qm "docs: with fr"
	t "readme + translation" HEAD~1 HEAD "docs: with fr" 0
	# case 4: README-only with escape hatch → pass
	echo w >> README.md && git add . && git commit -qm "docs: more

translations: lag-ok"
	t "escape hatch" HEAD~1 HEAD "docs: more

translations: lag-ok" 0

	[[ "$fail" == 0 ]] && echo "SELF-TEST PASS" || exit 1
	exit 0
fi

if [[ "${1:-}" == "--verify-headers" ]]; then
	missing=0
	for f in docs/translations/README_fr.md docs/translations/README_ja.md docs/translations/README_ko.md docs/translations/README_ru.md docs/translations/README_vi.md docs/translations/README_zh.md; do
		if ! head -5 "$f" | grep -q "translations: may-lag"; then
			echo "missing lag header: $f" >&2
			missing=1
		fi
	done
	exit "$missing"
fi

[[ -z "${BASE:-}" || -z "${HEAD:-}" ]] && { echo "BASE and HEAD are required" >&2; exit 2; }
check "$BASE" "$HEAD" "${COMMIT_MSGS:-}"
