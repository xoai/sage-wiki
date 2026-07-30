#!/usr/bin/env bash
# p4-fixture-server.sh — start a live sage-wiki fixture server for client
# contract tests and example smoke runs (P4-3/P4-4/P4-6).
#
# Seeds content through the write API itself (a summary, an article, an
# ontology entity) so NO LLM key is ever needed; BM25 search then returns
# results with zero LLM spend (vector search warns and disables — fine).
#
# Usage:
#   eval "$(scripts/p4-fixture-server.sh)"        # prints export lines
#   scripts/p4-fixture-server.sh --print          # same, explicit
#   scripts/p4-fixture-server.sh --stop           # kill a prior fixture
#
# Env overrides: FIXTURE_PORT (default random 34000-34999). The server
# stays running after a successful start — stop it with --stop.
set -euo pipefail

PIDFILE="${TMPDIR:-/tmp}/sage-wiki-p4-fixture.pid"
METAFILE="${TMPDIR:-/tmp}/sage-wiki-p4-fixture.env"

stop_fixture() {
	if [[ -f "$PIDFILE" ]]; then
		kill "$(cat "$PIDFILE")" 2>/dev/null || true
		rm -f "$PIDFILE" "$METAFILE"
	fi
}

case "${1:-}" in
--stop)
	stop_fixture
	echo "fixture stopped"
	exit 0
	;;
esac

# Idempotent re-run: kill a prior instance first.
stop_fixture

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d /tmp/sage-wiki-p4-fixture.XXXXXX)"
BIN="$WORK/sage-wiki"
WIKI="$WORK/wiki"
TOKEN="devtoken-$RANDOM$RANDOM"
PORT="${FIXTURE_PORT:-$((34000 + RANDOM % 1000))}"

CGO_ENABLED=0 go build -o "$BIN" "$REPO_ROOT/cmd/sage-wiki"

(
	# Strip provider keys from the fixture's environment — no LLM anywhere.
	unset OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY || true
	"$BIN" init "$WIKI" >/dev/null
	git -C "$WIKI" config user.email "fixture@sage-wiki.local"
	git -C "$WIKI" config user.name "sage-wiki fixture"
	printf '# Attention\nSelf-attention lets tokens weigh each other.\n' >"$WIKI/raw/attention.md"
	# A second raw source that is NEVER summarized stays uncompiled, so
	# queries for it surface uncompiled_sources > 0 (the compile-on-demand
	# signal the examples demonstrate).
	printf '# Recursion\nRecursion defines a function in terms of itself.\n' >"$WIKI/raw/recursion.md"
	# The fixture must not run the P2-3 compile worker: it would claim the
	# pending source, hold the coordinator lock, and make every submitted
	# compile job fail with "another compile holds the coordinator lock".
	# (Indented under the existing top-level `serve:` key — a second
	# top-level `serve:` is a YAML parse error.)
	printf '  worker:\n    enabled: false\n' >>"$WIKI/config.yaml"
	# One-shot compile to INDEX the raw sources (tier 0/1). Without an LLM
	# key/embedder the later tiers fail soft — index state is what we need:
	# it makes raw sources BM25-searchable and leaves them uncompiled, which
	# is the compile-on-demand signal the examples demonstrate.
	"$BIN" compile --project "$WIKI" >/dev/null 2>&1 || true
	# The one-shot compile advances every source to tier 3 even when the
	# summarize step fails (no LLM key) — which would leave NO uncompiled
	# source. Reset recursion to tier 1 (indexed, not compiled) so queries
	# for it surface uncompiled_sources > 0. (Fixture-only DB tweak;
	# python3 is guaranteed on ubuntu CI runners.)
	python3 - "$WIKI/.sage/wiki.db" <<'PY'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("UPDATE compile_items SET tier=1, status='pending' WHERE source_path='raw/recursion.md'")
db.commit()
PY
)

# Serve with provider keys stripped (same no-LLM guarantee as setup).
env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u GEMINI_API_KEY -u GOOGLE_API_KEY \
	SAGE_WIKI_TOKEN="$TOKEN" "$BIN" serve --project "$WIKI" --ui --port "$PORT" --bind 127.0.0.1 \
	>"$WORK/server.log" 2>&1 &
SERVER_PID=$!
echo "$SERVER_PID" >"$PIDFILE"

cleanup_on_error() {
	kill "$SERVER_PID" 2>/dev/null || true
	rm -f "$PIDFILE"
}
trap cleanup_on_error ERR

# On success the server stays up (that's the point) — kill it later with
# --stop or via $SAGE_WIKI_FIXTURE_PID.

# Wait for /v1/status (30s deadline).
BASE="http://127.0.0.1:$PORT"
deadline=$((SECONDS + 30))
until curl -sf -H "Authorization: Bearer $TOKEN" "$BASE/v1/status" >/dev/null 2>&1; do
	if ((SECONDS > deadline)); then
		echo "fixture: server did not become healthy in 30s" >&2
		tail -20 "$WORK/server.log" >&2 || true
		exit 1
	fi
	sleep 0.5
done

# Seed content through the write API (exercises auth + writes; no LLM).
AUTH="Authorization: Bearer $TOKEN"
CT="Content-Type: application/json"
curl -sf -H "$AUTH" -H "$CT" -X PUT "$BASE/v1/summaries" \
	-d '{"source":"attention.md","content":"Self-attention lets each token weigh every other token when building its representation.","concepts":"attention,transformer"}' >/dev/null
curl -sf -H "$AUTH" -H "$CT" -X PUT "$BASE/v1/articles/attention" \
	-d '{"content":"---\ntitle: Attention\n---\n# Attention\n\nSelf-attention computes pairwise token affinities.\n"}' >/dev/null
curl -sf -H "$AUTH" -H "$CT" -X POST "$BASE/v1/ontology/entities" \
	-d '{"id":"attention","type":"concept","name":"Attention"}' >/dev/null

# Sanity: the seeded content is retrievable (BM25 works without an embedder).
# NB: /v1/search result items carry PascalCase keys (untagged DocResult —
# pre-1.0 wire contract), so the probe greps "ID", not "id".
hits="$(curl -sf -H "$AUTH" "$BASE/v1/search?query=attention&limit=3" | grep -c '"ID"' || true)"
if [[ "$hits" -lt 1 ]]; then
	echo "fixture: seeded search returned no results" >&2
	exit 1
fi

# Sanity 2: the seeded article is readable at its output-relative path.
curl -sf -H "$AUTH" "$BASE/v1/articles/concepts/attention.md" >/dev/null

cat >"$METAFILE" <<EOF
export SAGE_WIKI_URL=$BASE
export SAGE_WIKI_TOKEN=$TOKEN
export SAGE_WIKI_FIXTURE_PID=$SERVER_PID
export SAGE_WIKI_FIXTURE_DIR=$WORK
EOF
cat "$METAFILE"
