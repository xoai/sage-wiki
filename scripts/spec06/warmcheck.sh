#!/usr/bin/env bash
# SPEC-06 warm-latency gate: BenchmarkSearchWarm_Mmap must be within 2x of
# BenchmarkSearchWarm_Memory on the same fixture. Opt-in CI (timing-sensitive);
# the blocking gates are TestResidentCeiling / TestInt8RecallAt10 /
# TestMmapParity_*.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=$(go test ./internal/vectors/ -bench 'BenchmarkSearchWarm_(Memory|Mmap)$' -benchtime 100x -run '^$' 2>/dev/null)
echo "$OUT"

mem=$(echo "$OUT" | awk '/BenchmarkSearchWarm_Memory/ {print $3}')
mmap=$(echo "$OUT" | awk '/BenchmarkSearchWarm_Mmap/ {print $3}')
if [[ -z "$mem" || -z "$mmap" ]]; then
  echo "warmcheck: could not parse benchmark output" >&2
  exit 1
fi
ratio=$(awk "BEGIN {printf \"%.3f\", $mmap / $mem}")
echo "warmcheck: mmap/memory warm ratio = $ratio (memory ${mem} ns/op, mmap ${mmap} ns/op)"
if awk "BEGIN {exit !($ratio > 2.0)}"; then
  echo "warmcheck: FAIL — warm mmap latency exceeds 2x the memory backend" >&2
  exit 1
fi
echo "warmcheck: PASS"
