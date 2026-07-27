#!/usr/bin/env python3
"""Verify that numbers quoted in REPORT.md match the results JSONs.

REPORT.md carries machine-checkable annotations:

    <!-- check:<results-stem> <dotted.json.path> = <expected> -->

e.g. `<!-- check:locomo_full metrics.overall.accuracy = 66.7 -->` asserts
that results/locomo_full.json's metrics.overall.accuracy rounds to 66.7
(one decimal). Exits non-zero on any mismatch, unknown file, or a report
containing no checks at all (unverified numbers are a failure, not a pass).
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

CHECK_RE = re.compile(
    r"<!--\s*check:(?P<stem>[\w-]+)\s+(?P<path>[\w.\-]+)\s*=\s*(?P<expected>-?[\d.]+)\s*-->"
)


def lookup(data, dotted: str):
    cur = data
    for part in dotted.split("."):
        if not isinstance(cur, dict) or part not in cur:
            raise KeyError(dotted)
        cur = cur[part]
    return cur


def main() -> int:
    parser = argparse.ArgumentParser()
    root = Path(__file__).resolve().parent
    parser.add_argument("--report", default=str(root / "REPORT.md"))
    parser.add_argument("--results-dir", default=str(root / "results"))
    args = parser.parse_args()

    report = Path(args.report).read_text(encoding="utf-8")
    results_dir = Path(args.results_dir)
    checks = list(CHECK_RE.finditer(report))
    if not checks:
        print("FAIL: report contains no <!-- check: --> annotations — numbers unverified")
        return 1

    failures = 0
    cache: dict[str, dict] = {}
    for m in checks:
        stem, path, expected = m.group("stem"), m.group("path"), m.group("expected")
        if stem not in cache:
            f = results_dir / f"{stem}.json"
            if not f.is_file():
                print(f"FAIL: {stem}: results file not found: {f}")
                failures += 1
                cache[stem] = {}
                continue
            cache[stem] = json.loads(f.read_text(encoding="utf-8"))
        if not cache[stem]:
            continue
        try:
            actual = lookup(cache[stem], path)
        except KeyError:
            print(f"FAIL: {stem}: no such path {path}")
            failures += 1
            continue
        try:
            ok = round(float(actual), 1) == round(float(expected), 1)
        except (TypeError, ValueError):
            ok = str(actual) == expected
        if ok:
            print(f"ok:   {stem} {path} = {expected}")
        else:
            try:
                shown = round(float(actual), 1)
            except (TypeError, ValueError):
                shown = actual
            print(f"FAIL: {stem} {path}: report says {expected}, results say {shown}")
            failures += 1

    print(f"{len(checks)} checks, {failures} failures")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
