"""T4 — metrics: grouped accuracy with infra_error exclusion, latency, tau-b, BEAM scores."""

import pytest

from benchmarks.common.metrics import (
    aggregate_accuracy,
    aggregate_beam,
    compute_kendall_tau_b,
    latency_stats,
    nugget_question_score,
)


def q(qid, group, score, status="ok", latency=10.0):
    return {"question_id": qid, "group": group, "score": score,
            "status": status, "search_latency_ms": latency}


class TestAccuracy:
    def test_overall_and_by_group(self):
        rows = [q("a", "single_hop", 1.0), q("b", "single_hop", 0.0),
                q("c", "temporal", 1.0)]
        m = aggregate_accuracy(rows)
        assert m["overall"]["total"] == 3 and m["overall"]["correct"] == 2
        assert m["overall"]["accuracy"] == pytest.approx(2 / 3 * 100)
        assert m["by_group"]["single_hop"]["accuracy"] == pytest.approx(50.0)
        assert m["by_group"]["temporal"]["accuracy"] == pytest.approx(100.0)

    def test_infra_error_excluded_from_denominator(self):
        # N=5 questions, k=2 infra_error → denominator 3
        rows = [q("a", "g", 1.0), q("b", "g", 1.0), q("c", "g", 0.0),
                q("d", "g", 0.0, status="infra_error"),
                q("e", "g", 0.0, status="infra_error")]
        m = aggregate_accuracy(rows)
        assert m["overall"]["total"] == 3
        assert m["overall"]["accuracy"] == pytest.approx(2 / 3 * 100)
        assert m["overall"]["infra_errors"] == 2

    def test_all_infra_error_yields_zero_denominator(self):
        rows = [q("a", "g", 0.0, status="infra_error")]
        m = aggregate_accuracy(rows)
        assert m["overall"]["total"] == 0
        assert m["overall"]["accuracy"] == 0.0
        assert m["overall"]["infra_errors"] == 1


class TestLatency:
    def test_p50_p95(self):
        rows = [q(str(i), "g", 1.0, latency=float(i)) for i in range(1, 101)]
        s = latency_stats(rows)
        assert s["p50_ms"] == pytest.approx(50.5, abs=1.0)
        assert s["p95_ms"] == pytest.approx(95.05, abs=1.0)
        assert s["count"] == 100

    def test_infra_error_latencies_excluded(self):
        rows = [q("a", "g", 1.0, latency=10.0),
                q("b", "g", 0.0, status="infra_error", latency=99999.0)]
        assert latency_stats(rows)["count"] == 1


class TestKendallTauB:
    def test_perfect_agreement(self):
        assert compute_kendall_tau_b([1, 2, 3, 4], [1, 2, 3, 4]) == pytest.approx(1.0)

    def test_perfect_disagreement(self):
        assert compute_kendall_tau_b([4, 3, 2, 1], [1, 2, 3, 4]) == pytest.approx(-1.0)

    def test_short_lists_zero(self):
        assert compute_kendall_tau_b([1], [1]) == 0.0
        assert compute_kendall_tau_b([], []) == 0.0

    def test_disjoint_lists_zero(self):
        assert compute_kendall_tau_b([1, 2], [3, 4]) == 0.0


class TestBeamScoring:
    def test_nugget_mean_is_primary(self):
        assert nugget_question_score([1.0, 0.5, 0.0]) == pytest.approx(0.5)
        assert nugget_question_score([]) == 0.0

    def test_aggregate_beam_by_type_with_tau_supplement(self):
        rows = [
            {"question_id": "a", "group": "event_ordering", "score": 0.5,
             "status": "ok", "tau_b": 1.0, "search_latency_ms": 5.0},
            {"question_id": "b", "group": "abstention", "score": 1.0,
             "status": "ok", "search_latency_ms": 5.0},
        ]
        m = aggregate_beam(rows)
        eo = m["by_group"]["event_ordering"]
        assert eo["avg_score"] == pytest.approx(50.0)  # nugget mean primary
        # score_with_tau = mean(nugget_mean, normalized tau) ; tau 1.0 → norm 1.0
        assert eo["score_with_tau"] == pytest.approx((0.5 + 1.0) / 2 * 100)
        assert m["by_group"]["abstention"]["avg_score"] == pytest.approx(100.0)
        assert "score_with_tau" not in m["by_group"]["abstention"]
