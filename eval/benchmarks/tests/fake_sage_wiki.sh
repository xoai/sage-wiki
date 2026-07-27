#!/usr/bin/env bash
# Scripted stand-in for the sage-wiki binary (offline tests only).
# Behavior selected via FAKE_SW_MODE; call counters live under FAKE_SW_STATE.
set -u
MODE="${FAKE_SW_MODE:-ok}"
STATE="${FAKE_SW_STATE:-/tmp/fake-sw-state}"
mkdir -p "$STATE"

bump() { # bump <counter-file> -> echoes new value
  local f="$STATE/$1" n=0
  [ -f "$f" ] && n=$(cat "$f")
  n=$((n + 1))
  echo "$n" > "$f"
  echo "$n"
}

CMD="${1:-}"

HYBRID_JSON='{"ok":true,"data":[{"ID":"m1","Content":"Alice adopted Biscuit.","Tags":null,"ArticlePath":"wiki/concepts/biscuit.md","BM25Rank":1,"VectorRank":2,"RRFScore":0.03},{"ID":"m2","Content":"Bob moved to Lisbon.","Tags":null,"ArticlePath":"wiki/concepts/lisbon.md","BM25Rank":2,"VectorRank":0,"RRFScore":0.01}]}'
BM25_JSON='{"ok":true,"data":[{"ID":"m1","Content":"x","Tags":null,"ArticlePath":"a.md","BM25Rank":1,"VectorRank":0,"RRFScore":0.02}]}'

case "$CMD" in
  version)
    echo "sage-wiki fake (commit test, built now)"
    ;;
  compile)
    n=$(bump compile-calls)
    case "$MODE" in
      compile-fail)
        echo "Error: boom" >&2
        exit 1
        ;;
      *)
        echo "Compile complete: 3 concepts, 3 articles, 0 errors"
        ;;
    esac
    ;;
  status)
    case "$MODE" in
      no-vectors) VC=0 ;;
      *) VC=42 ;;
    esac
    echo "{\"ok\":true,\"data\":{\"sources\":1,\"concepts\":3,\"vector_count\":$VC}}"
    ;;
  search)
    n=$(bump search-calls)
    case "$MODE" in
      null-data)
        echo '{"ok":true,"data":null}'
        ;;
      garbage-stdout)
        echo 'this is not json at all'
        ;;
      embed-warning)
        echo "warning: embed failed, using BM25-only: dial tcp: timeout" >&2
        echo "$BM25_JSON"
        ;;
      embed-warning-once)
        if [ "$n" -le 1 ]; then
          echo "warning: embed failed, using BM25-only: dial tcp: timeout" >&2
          echo "$BM25_JSON"
        else
          echo "$HYBRID_JSON"
        fi
        ;;
      bm25-only)
        echo "$BM25_JSON"
        ;;
      *)
        echo "$HYBRID_JSON"
        ;;
    esac
    ;;
  *)
    echo "fake sage-wiki: unknown command '$CMD'" >&2
    exit 2
    ;;
esac
