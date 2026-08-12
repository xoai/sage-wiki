#!/usr/bin/env bash
# check-determinism.sh — best-effort static tripwire for SPEC-04 D1.
#
# It flags two evidence-backed patterns:
#   (a) a file declares `X := map[` or `X := make(map[` and later ranges X
#   (b) a range over a known workspace map field (manifest Sources/
#       Concepts, DedupCache.cache) feeding a serializer
# …unless the line is allowlisted in scripts/determinism-allowlist.txt
# (one line per verified-safe site with its justification).
#
# LIMITS (documented in docs/determinism.md): no cross-file dataflow, no
# judgment about whether a sort's key is the right one. A tripwire, not a
# proof — the proofs are the double-compile tests.
set -uo pipefail

cd "$(dirname "$0")/.."
ALLOWLIST="${SAGE_DETERMINISM_ALLOWLIST:-scripts/determinism-allowlist.txt}"
PKGS="internal/compiler internal/manifest internal/wiki internal/sourcedate internal/ontology internal/mirror internal/pack internal/hub internal/export internal/serve internal/mcp internal/tui internal/vectors internal/parity"

violations() {
  # (a) file-local map declarations ranged later.
  for f in $(grep -rlE ':= (map\[|make\(map\[)' --include="*.go" $PKGS 2>/dev/null | grep -v "_test.go"); do
    names=$(grep -oE '[a-zA-Z_][a-zA-Z0-9_]* := (map\[|make\(map\[)' "$f" | sed 's/ :=.*//' | sort -u)
    for n in $names; do
      grep -nE "range ${n}\b" "$f" | sed "s|^|$f:|"
    done
  done
  # (b) known workspace map fields.
  grep -rnE 'range +(mf|m)\.(Sources|Concepts)\b|range +[a-zA-Z_]+\.cache\b' --include="*.go" $PKGS 2>/dev/null | grep -v "_test.go"
}

filter_allowed() {
  while IFS= read -r line; do
    loc="$(echo "$line" | cut -d: -f1,2)"
    if ! grep -qF "$loc" "$ALLOWLIST" 2>/dev/null; then
      echo "$line"
    fi
  done
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

  if [ "$fails" -ne 0 ]; then
    echo "self-test RED: $fails witness(es) failed against the current matcher"
    exit 1
  fi
  echo "self-test OK: planted candidates accepted and detected exactly; source-bound and reverse validation hold"
  exit 0
fi

v="$(violations | filter_allowed)"

if [ -n "$v" ]; then
  echo "check-determinism: map-range loops feeding writers without an allowlisted justification:"
  echo "$v"
  echo
  echo "Either sort before use (preferred) or add a justified line to $ALLOWLIST."
  echo "Best-effort check — see docs/determinism.md for its limits."
  exit 1
fi
echo "check-determinism: OK (all map-range loops near serializers sorted or allowlisted)"
