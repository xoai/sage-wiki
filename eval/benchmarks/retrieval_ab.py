#!/usr/bin/env python3
"""LLM-free retrieval A/B between two sage-wiki binaries.

Answers "did the search change improve retrieval?" without an answerer or a
judge, so it costs nothing and is fully deterministic.

Ground truth: every LOCOMO question carries `evidence` dia_ids (`D1:3` = turn 3
of session 1). Each session is ingested as `raw/session_N.md`, and search
results carry `src:raw/session_N.md` IDs, so a retrieval hit is checkable
against the session that actually holds the answer.

Metric: **evidence recall@k** — the fraction of questions whose top-k results
include at least one evidence session (`strict` = ALL evidence sessions).

Known bias: compiled concept articles are not session-tagged, so a concept hit
that genuinely contains the answer counts as a miss. This understates absolute
recall — but it understates BOTH binaries identically, so the A/B delta (the
number this script exists to produce) is unaffected.

Usage (from eval/):
    python -m benchmarks.retrieval_ab --old /path/sage-wiki-old --new ../sage-wiki \\
        --projects benchmarks/runs/locomo/full/projects --limits 10,50
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
import subprocess
import sys
import time
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

PKG_ROOT = Path(__file__).resolve().parent
DEFAULT_DATASET = PKG_ROOT / "runs" / "datasets" / "locomo" / "locomo10.json"
DIA_RE = re.compile(r"^D(\d+):")
SESSION_RE = re.compile(r"session_(\d+)")


def evidence_sessions(qa: dict) -> set[str]:
    """Session numbers named by a question's ground-truth evidence."""
    out = set()
    for ref in qa.get("evidence", []) or []:
        m = DIA_RE.match(str(ref))
        if m:
            out.add(m.group(1))
    return out


def retrieved_sessions(rows: list[dict]) -> set[str]:
    """Session numbers a result set touches (raw-source hits are traceable)."""
    out = set()
    for r in rows:
        for field in (r.get("ID", ""), r.get("ArticlePath", "") or ""):
            m = SESSION_RE.search(str(field))
            if m:
                out.add(m.group(1))
                break
    return out


def search(binary: str, project: Path, query: str, limit: int,
           timeout_s: int = 120) -> tuple[list[dict], float, str]:
    start = time.monotonic()
    proc = subprocess.run(
        [binary, "search", query, "--project", str(project),
         "--format", "json", "--limit", str(limit)],
        capture_output=True, text=True, timeout=timeout_s,
    )
    latency_ms = (time.monotonic() - start) * 1000
    if proc.returncode != 0:
        return [], latency_ms, f"exit {proc.returncode}: {proc.stderr[-160:]}"
    try:
        rows = json.loads(proc.stdout).get("data") or []
    except json.JSONDecodeError as exc:
        return [], latency_ms, f"undecodable stdout: {exc}"
    return rows, latency_ms, ""


def vector_leg_alive(rows: list[dict]) -> bool:
    """Any vector-ranked result means the hybrid vector leg contributed."""
    return any((r.get("VectorRank") or 0) > 0 for r in rows)


def evaluate(binary: str, label: str, dataset: list[dict], projects: Path,
             limits: list[int], conversations: list[int], categories: list[int],
             workers: int, max_questions: int | None) -> dict:
    items: list[tuple[int, Path, dict]] = []
    for conv_idx in conversations:
        entry = dataset[conv_idx]
        project = projects / f"conv{conv_idx}"
        if not project.is_dir():
            print(f"  [{label}] skipping conv{conv_idx}: no compiled project", file=sys.stderr)
            continue
        qs = [qa for qa in entry.get("qa", [])
              if qa.get("category") in categories and evidence_sessions(qa)]
        if max_questions is not None:
            qs = qs[:max_questions]
        items.extend((conv_idx, project, qa) for qa in qs)

    max_limit = max(limits)
    records: list[dict] = []

    def one(item):
        conv_idx, project, qa = item
        rows, latency_ms, err = search(binary, project, qa["question"], max_limit)
        want = evidence_sessions(qa)
        rec = {
            "conversation_idx": conv_idx,
            "category": qa["category"],
            "question": qa["question"],
            "evidence_sessions": sorted(want),
            "latency_ms": latency_ms,
            "error": err,
            "vector_leg": vector_leg_alive(rows),
            "returned": len(rows),
        }
        for k in limits:
            got = retrieved_sessions(rows[:k])
            rec[f"hit@{k}"] = bool(want & got)
            rec[f"strict@{k}"] = want.issubset(got) if want else False
        return rec

    with ThreadPoolExecutor(max_workers=workers) as pool:
        for i, rec in enumerate(pool.map(one, items), 1):
            records.append(rec)
            if i % 100 == 0:
                print(f"  [{label}] {i}/{len(items)}", file=sys.stderr)

    return summarize(label, records, limits)


def summarize(label: str, records: list[dict], limits: list[int]) -> dict:
    scored = [r for r in records if not r["error"]]
    by_cat: dict[int, list[dict]] = defaultdict(list)
    for r in scored:
        by_cat[r["category"]].append(r)

    def rate(rows: list[dict], key: str) -> float:
        return (sum(1 for r in rows if r[key]) / len(rows) * 100) if rows else 0.0

    out = {
        "label": label,
        "questions": len(records),
        "scored": len(scored),
        "errors": len(records) - len(scored),
        "vector_leg_alive": sum(1 for r in scored if r["vector_leg"]),
        "latency_p50_ms": statistics.median([r["latency_ms"] for r in scored]) if scored else 0.0,
        "overall": {f"recall@{k}": rate(scored, f"hit@{k}") for k in limits},
        "strict": {f"recall@{k}": rate(scored, f"strict@{k}") for k in limits},
        "by_category": {
            str(cat): {f"recall@{k}": rate(rows, f"hit@{k}") for k in limits}
            for cat, rows in sorted(by_cat.items())
        },
        "records": records,
    }
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description="LLM-free retrieval A/B for sage-wiki search")
    ap.add_argument("--old", required=True, help="baseline binary")
    ap.add_argument("--new", required=True, help="candidate binary")
    ap.add_argument("--projects", required=True, help="dir of compiled convN projects")
    ap.add_argument("--dataset", default=str(DEFAULT_DATASET))
    ap.add_argument("--limits", default="10,50")
    ap.add_argument("--conversations", default="0-9")
    ap.add_argument("--categories", default="1,2,3,4")
    ap.add_argument("--workers", type=int, default=8)
    ap.add_argument("--max-questions", type=int, default=None)
    ap.add_argument("--out", default=str(PKG_ROOT / "results" / "retrieval_ab.json"))
    args = ap.parse_args()

    limits = sorted(int(x) for x in args.limits.split(","))
    convs: list[int] = []
    for part in args.conversations.split(","):
        if "-" in part:
            lo, hi = part.split("-", 1)
            convs.extend(range(int(lo), int(hi) + 1))
        elif part:
            convs.append(int(part))
    cats = [int(c) for c in args.categories.split(",")]
    dataset = json.loads(Path(args.dataset).read_text(encoding="utf-8"))
    projects = Path(args.projects)

    results = {}
    for label, binary in (("old", args.old), ("new", args.new)):
        print(f"evaluating {label}: {binary}", file=sys.stderr)
        results[label] = evaluate(binary, label, dataset, projects, limits,
                                  convs, cats, args.workers, args.max_questions)

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    slim = {
        lbl: {k: v for k, v in res.items() if k != "records"}
        for lbl, res in results.items()
    }
    slim["deltas"] = {
        f"recall@{k}": round(results["new"]["overall"][f"recall@{k}"]
                             - results["old"]["overall"][f"recall@{k}"], 2)
        for k in limits
    }
    out_path.write_text(json.dumps(slim, indent=1), encoding="utf-8")

    print(f"\n{'metric':22s} {'old':>8s} {'new':>8s} {'delta':>8s}")
    for k in limits:
        o = results["old"]["overall"][f"recall@{k}"]
        n = results["new"]["overall"][f"recall@{k}"]
        print(f"{'evidence recall@' + str(k):22s} {o:7.1f}% {n:7.1f}% {n - o:+7.1f}pp")
    for k in limits:
        o = results["old"]["strict"][f"recall@{k}"]
        n = results["new"]["strict"][f"recall@{k}"]
        print(f"{'strict (all ev)@' + str(k):22s} {o:7.1f}% {n:7.1f}% {n - o:+7.1f}pp")
    print(f"\nquestions scored: old {results['old']['scored']}, new {results['new']['scored']}"
          f" (errors: {results['old']['errors']}/{results['new']['errors']})")
    print(f"vector leg alive: old {results['old']['vector_leg_alive']}, "
          f"new {results['new']['vector_leg_alive']} of scored")
    print(f"search p50: old {results['old']['latency_p50_ms']:.0f}ms, "
          f"new {results['new']['latency_p50_ms']:.0f}ms")
    print(f"\nwritten to {out_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
