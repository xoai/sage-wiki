#!/usr/bin/env bash
# extract-changelog.sh <tag> [changelog]
#
# Prints the CHANGELOG section body for a given version, for use as a GitHub
# Release description. The tag may be "vX.Y.Z" or "X.Y.Z"; a leading "v" is
# stripped because CHANGELOG headings are "## X.Y.Z — DATE" (no "v").
#
# Matches the version EXACTLY (so 0.1.9 does not match 0.1.95) and prints every
# line after the "## X.Y.Z ..." heading up to (but excluding) the next "## "
# heading. The heading line itself is omitted. Exits 0 even when the section is
# absent (stdout is then empty) so the caller can apply a fallback.
set -euo pipefail

tag="${1:?usage: extract-changelog.sh <tag> [changelog]}"
changelog="${2:-CHANGELOG.md}"
version="${tag#v}"

awk -v ver="$version" '
  BEGIN {
    # Escape regex metacharacters that can appear in a semver (".", "+") so the
    # match is literal; the version is then anchored and must be followed by a
    # space/tab, an em-dash, or end-of-line.
    gsub(/[.+]/, "\\\\&", ver)
    re = "^## " ver "([ \t]|$|\xe2\x80\x94)"
    in_section = 0
  }
  /^## / {
    if (in_section) exit            # next section heading -> stop
    if ($0 ~ re) { in_section = 1; next }   # our heading -> start (skip heading)
  }
  in_section { print }
' "$changelog"
