#!/usr/bin/env bash
# check-determinism.sh — best-effort static tripwire for SPEC-04 D1.
#
# It flags two evidence-backed patterns:
#   (a) a file declares `X := map[` or `X := make(map[` and later ranges X
#   (b) a range over a known workspace map field (manifest Sources/
#       Concepts, DedupCache.cache) feeding a serializer
# …unless the site is allowlisted in scripts/determinism-allowlist.txt.
# Matching is source-bound and bidirectional: an entry identifies a site by
# <location>|<source> and the check validates BOTH directions — an
# unallowlisted candidate fails, and a dead, duplicate, or malformed entry
# fails too (a stale line number here is itself a finding).
#
# LIMITS (documented in docs/determinism.md): no cross-file dataflow, no
# judgment about whether a sort's key is the right one. A tripwire, not a
# proof — the proofs are the double-compile tests.
set -uo pipefail

# Capture the absolute script path before any cwd change: $0 may be relative,
# so deriving repo root or a recursive invocation path after `cd` would
# resolve against the new directory instead of the script's own.
script_path="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
cd "$(dirname "$script_path")/.."
ALLOWLIST="${SAGE_DETERMINISM_ALLOWLIST:-scripts/determinism-allowlist.txt}"
PKGS="internal/compiler internal/manifest internal/wiki internal/sourcedate internal/ontology internal/mirror internal/pack internal/hub internal/export internal/serve internal/mcp internal/tui internal/vectors internal/parity"

violations() {
  # "Emit zero or more candidates" is success: each discovery grep below
  # exits 1 on a zero-match run by design, and that must not poison the
  # pipeline status of `violations | filter_allowed` under pipefail — the
  # status is filter_allowed's alone.
  # (a) file-local map declarations ranged later.
  for f in $(grep -rlE ':= (map\[|make\(map\[)' --include="*.go" $PKGS 2>/dev/null | grep -v "_test.go" || true); do
    names=$(grep -oE '[a-zA-Z_][a-zA-Z0-9_]* := (map\[|make\(map\[)' "$f" | sed 's/ :=.*//' | sort -u || true)
    for n in $names; do
      grep -nE "range ${n}\b" "$f" | sed "s|^|$f:|" || true
    done
  done
  # (b) known workspace map fields.
  grep -rnE 'range +(mf|m)\.(Sources|Concepts)\b|range +[a-zA-Z_]+\.cache\b' --include="*.go" $PKGS 2>/dev/null | grep -v "_test.go" || true
}

filter_allowed() {
  # Source-bound, bidirectional allowlist validation.
  #
  # Entry format: <location>|<source>|<justification>
  #   location      = the emitted violation's first two fields (<file>:<line>)
  #   source        = the emitted violation's text after the second colon,
  #                   trimmed of leading/trailing blanks — identity, together
  #                   with the location, of the site; colons, braces, and
  #                   trailing comments are preserved
  #   justification = free text, excluded from comparison
  # `|` is never escaped: any candidate source or entry field containing an
  # unescaped `|` is malformed. Blank lines and lines whose first
  # non-whitespace char is `#` are ignored; every data line must have exactly
  # three non-empty fields. A duplicate key, a malformed line, or an entry
  # key matching no candidate (dead) hard-fails with exit 1.
  local cand keys candkeys
  cand="$(mktemp)"
  keys="$(mktemp)"
  candkeys="$(mktemp)"
  trap 'rm -f "$cand" "$keys" "$candkeys"' RETURN
  # Detect a missing/unreadable allowlist before parsing: a redirection
  # error here would otherwise surface as "every candidate is
  # unallowlisted" and misdirect the operator at a nonexistent path. A
  # character device (/dev/null) is a legitimate empty allowlist.
  if [ ! -r "$ALLOWLIST" ]; then
    echo "check-determinism: allowlist missing or unreadable: $ALLOWLIST" >&2
    return 1
  fi
  if [ ! -f "$ALLOWLIST" ] && [ ! -c "$ALLOWLIST" ]; then
    echo "check-determinism: allowlist is not a regular file or character device: $ALLOWLIST" >&2
    return 1
  fi
  cat > "$cand"
  bad=0

  while IFS= read -r line || [ -n "$line" ]; do
    line="$(printf '%s' "$line" | sed 's/^[[:blank:]]*//; s/[[:blank:]]*$//')"
    [ -z "$line" ] && continue
    case "$line" in \#*) continue ;; esac
    if [ "$(printf '%s' "$line" | grep -o '|' | wc -l)" -ne 2 ]; then
      echo "check-determinism: malformed allowlist line (expected <location>|<source>|<justification>; \`|\` is never escaped): $line"
      bad=1
      continue
    fi
    loc="${line%%|*}"
    src="${line#*|}"; src="${src%%|*}"
    just="${line##*|}"
    if [ -z "$loc" ] || [ -z "$src" ] || [ -z "$just" ]; then
      echo "check-determinism: allowlist entry with an empty field (location, source, and justification are all required): $line"
      bad=1
      continue
    fi
    if grep -Fqx "$loc|$src" "$keys"; then
      echo "check-determinism: duplicate allowlist key (location+source identity): $loc|$src"
      bad=1
      continue
    fi
    printf '%s\n' "$loc|$src" >> "$keys"
  done < "$ALLOWLIST"

  while IFS= read -r line || [ -n "$line" ]; do
    loc="$(printf '%s' "$line" | cut -d: -f1,2)"
    src="$(printf '%s' "$line" | cut -d: -f3- | sed 's/^[[:blank:]]*//; s/[[:blank:]]*$//')"
    if [ -z "$src" ]; then
      echo "check-determinism: malformed candidate (no source text after the location): $line"
      bad=1
      continue
    fi
    case "$src" in
      *\|*) echo "check-determinism: malformed candidate (unescaped \`|\` in source): $line"; bad=1 ;;
    esac
    printf '%s\n' "$loc|$src" >> "$candkeys"
    if ! grep -Fqx "$loc|$src" "$keys"; then
      printf '%s\n' "$line"
    fi
  done < "$cand"

  while IFS= read -r key; do
    if ! grep -Fqx "$key" "$candkeys"; then
      echo "check-determinism: dead allowlist entry — no candidate matches this location+source: $key"
      bad=1
    fi
  done < "$keys"

  return "$bad"
}

if [ "${1:-}" = "--self-test" ]; then
  plant="internal/compiler/zz_determinism_selftest.go"
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir" "$plant"' EXIT
  # Planted candidates only: a map-declared range at a known line, the same
  # source at a second location, and a trailing-comment statement. The live
  # repository allowlist is never read or copied here, so live drift cannot
  # mask witness results.
  cat > "$plant" <<'EOF'
package compiler

func zzDeterminismSelfTest() []string {
	seen := map[string]bool{}
	out := []string{}
	for k := range seen {
		out = append(out, k)
	}
	extra := map[string]bool{}
	for k := range seen {
		out = append(out, k)
	}
	for k := range extra { // deterministic self-test
		out = append(out, k)
	}
	return out
}
EOF
  planted_candidates() { violations | grep -F "$plant"; }
  fails=0
  witness_fail() {
    echo "self-test FAIL: $1"
    fails=$((fails + 1))
  }
  # Temporary fixture allowlists, one per witness.
  cat > "$tmpdir/good" <<'EOF'
# a comment line and the blank line below must be ignored
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|exact key, first of two identical sources
internal/compiler/zz_determinism_selftest.go:10|for k := range seen {|identical source at distinct location
internal/compiler/zz_determinism_selftest.go:13|for k := range extra { // deterministic self-test|full trailing-comment statement

EOF
  cat > "$tmpdir/wrong_source" <<'EOF'
internal/compiler/zz_determinism_selftest.go:6|for k := range OTHER {|wrong source occupying a candidate's location
internal/compiler/zz_determinism_selftest.go:10|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:13|for k := range extra { // deterministic self-test|valid
EOF
  cat > "$tmpdir/dead" <<'EOF'
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:10|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:13|for k := range extra { // deterministic self-test|valid
internal/compiler/zz_determinism_selftest.go:999|for k := range seen {|dead entry: no candidate at this location
EOF
  cat > "$tmpdir/malformed" <<'EOF'
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:10|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:13|for k := range extra { // deterministic self-test|valid
not an allowlist entry: no delimiters
internal/compiler/zz_determinism_selftest.go:6|for k := range a|b {|unescaped pipe in source
EOF
  cat > "$tmpdir/duplicate" <<'EOF'
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|first duplicate
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|second duplicate
internal/compiler/zz_determinism_selftest.go:10|for k := range seen {|valid
internal/compiler/zz_determinism_selftest.go:13|for k := range extra { // deterministic self-test|valid
EOF
  : > "$tmpdir/empty"

  # RED witnesses: the location-only matcher must fail these.
  # A same-location wrong-source entry must not exempt the candidate.
  ALLOWLIST="$tmpdir/wrong_source"
  if [ -z "$(planted_candidates | filter_allowed | grep -F 'zz_determinism_selftest.go:6')" ]; then
    witness_fail "same-location wrong-source entry exempted the candidate — source-bound matching missing"
  fi
  # Dead, malformed, and duplicate-key entries must each fail validation.
  ALLOWLIST="$tmpdir/dead"
  if [ -z "$(planted_candidates | filter_allowed)" ]; then
    witness_fail "dead allowlist entry accepted — reverse validation missing"
  fi
  ALLOWLIST="$tmpdir/malformed"
  if [ -z "$(planted_candidates | filter_allowed)" ]; then
    witness_fail "malformed allowlist entry accepted — entry validation missing"
  fi
  ALLOWLIST="$tmpdir/duplicate"
  if [ -z "$(planted_candidates | filter_allowed)" ]; then
    witness_fail "duplicate-key allowlist entry accepted — key validation missing"
  fi

  # GREEN-only witnesses: acceptance surface the implemented matcher must keep.
  ALLOWLIST="$tmpdir/good"
  if [ -n "$(planted_candidates | filter_allowed)" ]; then
    witness_fail "exact key, trailing-comment statement, or identical-source entries not accepted; blank/comment lines not ignored"
  fi
  ALLOWLIST="$tmpdir/empty"
  detected="$(planted_candidates | filter_allowed)"
  if [ "$(printf '%s\n' "$detected" | grep -cF 'zz_determinism_selftest')" -lt 3 ]; then
    witness_fail "unallowlisted planted offender was not detected"
  fi
  # Zero-candidate family witness: a discovery pass whose final grep finds
  # nothing exits 1 by design — "emit zero or more candidates" must not be
  # read as an allowlist validation failure. Discovery over an empty package
  # dir with a valid (empty) allowlist must yield the pipeline status of
  # filter_allowed alone, i.e. 0.
  mkdir -p "$tmpdir/nomatch"
  PKGS_SAVE="$PKGS"
  PKGS="$tmpdir/nomatch"
  ALLOWLIST="$tmpdir/empty"
  if ! v="$(violations | filter_allowed)"; then
    witness_fail "zero-candidate discovery exit poisoned allowlist validation status"
  fi
  PKGS="$PKGS_SAVE"

  # RED witness: a final candidate line without a trailing newline must
  # still be processed — dropping it would surface as a false dead entry.
  cat > "$tmpdir/single" <<'EOF'
internal/compiler/zz_determinism_selftest.go:6|for k := range seen {|single non-newline-terminated candidate
EOF
  ALLOWLIST="$tmpdir/single"
  if ! printf '%s' 'internal/compiler/zz_determinism_selftest.go:6:for k := range seen {' | filter_allowed >/dev/null 2>&1; then
    witness_fail "final candidate without trailing newline dropped — false dead entry"
  fi

  # END-TO-END RED witness: a missing allowlist must fail on the script's
  # real caller path — rc=1, the accurate missing/unreadable diagnostic,
  # and a truthful generic summary. The misleading "dead, duplicate, or
  # malformed" claim and any unallowlisted guidance must never appear.
  # Recursive invocation via "$script_path" without --self-test exercises
  # the normal path; the env var drives it exactly like a CI invocation.
  e2e_out="$(SAGE_DETERMINISM_ALLOWLIST="$tmpdir/does-not-exist" bash "$script_path" 2>&1)"
  e2e_rc=$?
  if [ "$e2e_rc" -ne 1 ]; then
    witness_fail "missing allowlist on caller path: rc=$e2e_rc, want 1"
  elif ! printf '%s\n' "$e2e_out" | grep -qF "check-determinism: allowlist missing or unreadable: $tmpdir/does-not-exist"; then
    witness_fail "missing allowlist on caller path: accurate diagnostic not emitted"
  elif printf '%s\n' "$e2e_out" | grep -qE 'dead, duplicate, or malformed|unallowlisted'; then
    witness_fail "missing allowlist on caller path: misleading summary or unallowlisted guidance emitted"
  fi

  if [ "$fails" -ne 0 ]; then
    echo "self-test RED: $fails witness(es) failed against the current matcher"
    exit 1
  fi
  echo "self-test OK: planted candidates accepted and detected exactly; source-bound and reverse validation hold"
  exit 0
fi

v="$(violations | filter_allowed)"
rc=$?

if [ "$rc" -ne 0 ]; then
  [ -n "$v" ] && echo "$v"
  echo "check-determinism: allowlist validation FAILED (see above)."
  exit 1
fi

if [ -n "$v" ]; then
  echo "check-determinism: map-range loops feeding writers without an allowlisted justification:"
  echo "$v"
  echo
  echo "Either sort before use (preferred) or add a justified line to $ALLOWLIST."
  echo "Best-effort check — see docs/determinism.md for its limits."
  exit 1
fi
echo "check-determinism: OK (no unallowlisted candidate found by the grep patterns above)"
