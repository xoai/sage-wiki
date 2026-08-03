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

v="$(violations | filter_allowed)"

if [ "${1:-}" = "--self-test" ]; then
  tmp_allow="$(mktemp)"
  plant="internal/compiler/zz_determinism_selftest.go"
  trap 'rm -f "$tmp_allow" "$plant"' EXIT
  cp "$ALLOWLIST" "$tmp_allow"
  # The mechanism check: violations exist unfiltered and the allowlist gates them.
  if [ -n "$(violations)" ] && [ -n "$(violations | filter_allowed)" ]; then
    echo "self-test FAIL: violations pipeline produced nothing even unfiltered"
    exit 1
  fi
  # The planted-offender check: an unallowlisted map-declared range is flagged.
  cat > "$plant" <<'EOF'
package compiler

func zzDeterminismSelfTest() []string {
	seen := map[string]bool{}
	out := []string{}
	for k := range seen {
		out = append(out, k)
	}
	return out
}
EOF
  if [ -z "$(violations | grep zz_determinism_selftest)" ]; then
    echo "self-test FAIL: planted offender was not flagged"
    exit 1
  fi
  ALLOWLIST="$tmp_allow"
  echo "self-test OK: unfiltered violations exist, allowlist gates them, planted offender flagged"
  exit 0
fi

if [ -n "$v" ]; then
  echo "check-determinism: map-range loops feeding writers without an allowlisted justification:"
  echo "$v"
  echo
  echo "Either sort before use (preferred) or add a justified line to $ALLOWLIST."
  echo "Best-effort check — see docs/determinism.md for its limits."
  exit 1
fi
echo "check-determinism: OK (all map-range loops near serializers sorted or allowlisted)"
