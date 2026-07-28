"""Aggregate metrics.

Rows are per-question dicts with: question_id, group (category/type), score
(0..1), status ("ok" | "infra_error"), search_latency_ms, and optionally
tau_b (BEAM event_ordering).

infra_error questions (compile hard-fail, persistent degraded search) are
EXCLUDED from accuracy denominators and counted separately (spec §5) —
infrastructure reliability must not masquerade as memory quality.

compute_kendall_tau_b is vendored from mem0ai/memory-benchmarks
(benchmarks/common/metrics.py, Apache-2.0 — see NOTICE).
"""

from __future__ import annotations

import statistics
from collections import defaultdict


def _scored(rows: list[dict]) -> list[dict]:
    return [r for r in rows if r.get("status", "ok") != "infra_error"]


def _bucket(scored: list[dict]) -> dict:
    correct = sum(1 for r in scored if r.get("score", 0.0) >= 0.5)
    total = len(scored)
    return {
        "total": total,
        "correct": correct,
        "accuracy": correct / total * 100 if total else 0.0,
        "avg_score": statistics.mean(r.get("score", 0.0) for r in scored) * 100 if scored else 0.0,
    }


def aggregate_accuracy(rows: list[dict]) -> dict:
    scored = _scored(rows)
    overall = _bucket(scored)
    overall["infra_errors"] = len(rows) - len(scored)

    groups: dict[str, list[dict]] = defaultdict(list)
    for r in scored:
        groups[r.get("group", "unknown")].append(r)
    return {
        "overall": overall,
        "by_group": {g: _bucket(rs) for g, rs in sorted(groups.items())},
    }


def aggregate_beam(rows: list[dict]) -> dict:
    """BEAM aggregation: nugget-mean is the primary score for every type;
    event_ordering additionally reports score_with_tau (upstream parity:
    mean of nugget-mean and (tau_b+1)/2)."""
    m = aggregate_accuracy(rows)
    scored = _scored(rows)
    groups: dict[str, list[dict]] = defaultdict(list)
    for r in scored:
        groups[r.get("group", "unknown")].append(r)
    for g, rs in groups.items():
        taus = [r["tau_b"] for r in rs if "tau_b" in r]
        if taus:
            combined = [
                (r.get("score", 0.0) + (r["tau_b"] + 1.0) / 2.0) / 2.0
                for r in rs if "tau_b" in r
            ]
            m["by_group"][g]["tau_b_avg"] = statistics.mean(taus)
            m["by_group"][g]["score_with_tau"] = statistics.mean(combined) * 100
    return m


def latency_stats(rows: list[dict]) -> dict:
    vals = sorted(
        r["search_latency_ms"] for r in _scored(rows)
        if isinstance(r.get("search_latency_ms"), (int, float))
    )
    if not vals:
        return {"count": 0, "p50_ms": 0.0, "p95_ms": 0.0, "avg_ms": 0.0}

    def pct(p: float) -> float:
        if len(vals) == 1:
            return vals[0]
        idx = p / 100 * (len(vals) - 1)
        lo, hi = int(idx), min(int(idx) + 1, len(vals) - 1)
        frac = idx - lo
        return vals[lo] * (1 - frac) + vals[hi] * frac

    return {
        "count": len(vals),
        "p50_ms": pct(50),
        "p95_ms": pct(95),
        "avg_ms": statistics.mean(vals),
    }


def nugget_question_score(nugget_scores: list[float]) -> float:
    return statistics.mean(nugget_scores) if nugget_scores else 0.0


def compute_kendall_tau_b(predicted_order: list[int], reference_order: list[int]) -> float:
    """Kendall tau-b rank correlation, vendored verbatim from mem0's harness."""
    if len(predicted_order) < 2 or len(reference_order) < 2:
        return 0.0

    pred_rank = {v: i for i, v in enumerate(predicted_order)}
    ref_rank = {v: i for i, v in enumerate(reference_order)}

    common = set(predicted_order) & set(reference_order)
    items = sorted(common)
    if len(items) < 2:
        return 0.0

    concordant = discordant = tied_pred = tied_ref = 0
    for i in range(len(items)):
        for j in range(i + 1, len(items)):
            a, b = items[i], items[j]
            pred_diff = pred_rank[a] - pred_rank[b]
            ref_diff = ref_rank[a] - ref_rank[b]
            if pred_diff == 0 and ref_diff == 0:
                tied_pred += 1
                tied_ref += 1
            elif pred_diff == 0:
                tied_pred += 1
            elif ref_diff == 0:
                tied_ref += 1
            elif (pred_diff > 0 and ref_diff > 0) or (pred_diff < 0 and ref_diff < 0):
                concordant += 1
            else:
                discordant += 1

    n1 = concordant + discordant + tied_pred
    n2 = concordant + discordant + tied_ref
    if n1 == 0 or n2 == 0:
        return 0.0
    return (concordant - discordant) / ((n1 * n2) ** 0.5)
