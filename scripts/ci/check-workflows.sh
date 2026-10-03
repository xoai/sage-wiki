#!/usr/bin/env bash
# check-workflows.sh — GitHub Actions guards for failure classes this
# repo has actually shipped.
#
# Guard 1 (the #206 class): a '#' comment inside a folded scalar block
# (`if: >-` etc.) is PART OF THE STRING, not a comment — for `if:`
# conditions it corrupts the expression into permanent-false and the
# job silently SKIPS forever (shipped: docker-main publication halted
# for two merges before anyone noticed).
#
# Usage: check-workflows.sh [scan|self-test|check]   (default: check)
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

python3 - "$ROOT" "${1:-check}" <<'PYEOF'
import glob
import os
import sys
import tempfile

FOLDED_MARK = set(">-+")


def scan_dir(directory):
    """Yield (file, line_no, line) for every comment inside a folded
    block belonging to an `if:` key."""
    for f in sorted(glob.glob(os.path.join(directory, "*.yml"))
                    + glob.glob(os.path.join(directory, "*.yaml"))):
        in_block, indent = False, 0
        for n, line in enumerate(open(f, encoding="utf-8"), 1):
            if in_block:
                if line.strip():
                    lindent = len(line) - len(line.lstrip())
                    if lindent <= indent:
                        in_block = False
                    elif line.lstrip().startswith("#"):
                        yield f, n, line.strip()
                continue
            s = line.rstrip()
            if ":" in s:
                key, _, value = s.partition(":")
                value = value.strip()
                if (key.strip() == "if" and value
                        and set(value) <= FOLDED_MARK):
                    in_block = True
                    # The block ends at the first non-blank line at or below
                    # the KEY's own indent (a sibling key) — block content
                    # sits strictly deeper (typically key+2).
                    indent = len(s) - len(s.lstrip())


def report(directory, to_stderr=True):
    viol = list(scan_dir(directory))
    sink = sys.stderr if to_stderr else sys.stdout
    for f, n, line in viol:
        print(f"VIOLATION: {os.path.basename(f)}:{n}: comment inside a "
              f"folded if-block corrupts the expression (#206 class): {line}",
              file=sink)
    return viol


def self_test():
    """A checker that cannot fail is not a checker: the guard must catch
    the canonical bad shape and pass the canonical good one."""
    with tempfile.TemporaryDirectory() as tmp:
        bad = os.path.join(tmp, "bad")
        good = os.path.join(tmp, "good")
        os.makedirs(bad)
        os.makedirs(good)
        with open(os.path.join(bad, "w.yml"), "w") as f:
            f.write("jobs:\n  x:\n    if: >-\n      a && b\n"
                    "      # comment inside folded block\n      && c\n"
                    "    runs-on: ubuntu-latest\n")
        with open(os.path.join(good, "w.yml"), "w") as f:
            f.write("jobs:\n  x:\n    # comment above the block is fine\n"
                    "    if: >-\n      a && b && c\n    runs-on: ubuntu-latest\n")
        if not list(scan_dir(bad)):
            print("check-workflows self-test: FAILED to catch the canonical "
                  "bad shape (guard is vacuous)", file=sys.stderr)
            return 1
        if list(scan_dir(good)):
            print("check-workflows self-test: FAILED on the canonical "
                  "good shape", file=sys.stderr)
            return 1
    print("check-workflows self-test: OK (catches bad, passes good)")
    return 0


def main():
    mode = sys.argv[2]
    if mode == "self-test":
        return self_test()
    if mode == "scan":
        viol = report(os.path.join(sys.argv[1], ".github", "workflows"))
        if viol:
            print(f"check-workflows: FAILED ({len(viol)} violation(s))",
                  file=sys.stderr)
            return 1
        print("check-workflows: OK")
        return 0
    if mode == "check":
        if self_test() != 0:
            return 1
        return main_with(sys.argv[1], "scan")
    print(f"usage: {sys.argv[0]} [scan|self-test|check]", file=sys.stderr)
    return 2


def main_with(root, mode):
    sys.argv = [sys.argv[0], root, mode]
    return main()


if __name__ == "__main__":
    sys.exit(main())
PYEOF
