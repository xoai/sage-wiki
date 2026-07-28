"""Shared rate-limit gate: cooperative backoff across workers + clean abort."""

import threading

import pytest

from benchmarks.common.ratelimit import QuotaExhausted, RateLimitGate


class FakeClock:
    """Deterministic monotonic clock; sleep() advances it instead of blocking."""

    def __init__(self):
        self.t = 1000.0
        self.slept = []

    def now(self):
        return self.t

    def sleep(self, seconds):
        self.slept.append(seconds)
        self.t += seconds


def gate(**kw):
    c = FakeClock()
    kw.setdefault("jitter", lambda: 0.0)
    return RateLimitGate(sleep=c.sleep, clock=c.now, **kw), c


class TestBackoff:
    def test_first_limit_sets_base_cooldown(self):
        g, c = gate(base_delay=2.0)
        assert g.report_limit() == pytest.approx(2.0)
        g.wait()
        assert c.slept == [pytest.approx(2.0)]

    def test_consecutive_limits_escalate_exponentially(self):
        g, _ = gate(base_delay=2.0, max_delay=100.0)
        assert [g.report_limit() for _ in range(4)] == [2.0, 4.0, 8.0, 16.0]

    def test_delay_is_capped(self):
        g, _ = gate(base_delay=2.0, max_delay=10.0)
        assert [g.report_limit() for _ in range(5)][-1] == 10.0

    def test_retry_after_header_wins_when_longer(self):
        g, _ = gate(base_delay=2.0)
        assert g.report_limit(retry_after=45.0) == pytest.approx(45.0)

    def test_success_resets_escalation(self):
        g, _ = gate(base_delay=2.0)
        g.report_limit(); g.report_limit()
        g.report_success()
        assert g.report_limit() == pytest.approx(2.0)

    def test_wait_is_noop_when_not_cooling(self):
        g, c = gate()
        g.wait()
        assert c.slept == []


class TestSharedBackpressure:
    def test_one_workers_limit_pauses_the_others(self):
        g, c = gate(base_delay=5.0)
        g.report_limit()          # worker A hits a 429
        g.wait()                  # worker B must wait out A's cooldown
        assert c.slept == [pytest.approx(5.0)]

    def test_thread_safe_under_concurrent_reports(self):
        g, _ = gate(base_delay=1.0, max_delay=1.0, give_up_after=1e9)
        errors = []

        def hammer():
            try:
                for _ in range(50):
                    g.report_limit()
                    g.report_success()
            except Exception as exc:  # noqa: BLE001
                errors.append(exc)

        threads = [threading.Thread(target=hammer) for _ in range(8)]
        for t in threads: t.start()
        for t in threads: t.join()
        assert errors == []


class TestQuotaAbort:
    def test_sustained_limiting_raises_quota_exhausted(self):
        g, _ = gate(base_delay=10.0, max_delay=10.0, give_up_after=25.0)
        g.report_limit()   # 10s cumulative
        g.report_limit()   # 20s
        with pytest.raises(QuotaExhausted, match="resume"):
            g.report_limit()   # 30s > 25s budget

    def test_success_clears_the_abort_budget(self):
        g, _ = gate(base_delay=10.0, max_delay=10.0, give_up_after=25.0)
        g.report_limit(); g.report_limit()
        g.report_success()
        g.report_limit(); g.report_limit()   # budget restarted — no raise

    def test_stats_expose_what_happened(self):
        g, _ = gate(base_delay=2.0)
        g.report_limit(); g.report_limit(); g.report_success()
        s = g.stats()
        assert s["rate_limit_events"] == 2 and s["total_wait_seconds"] == pytest.approx(6.0)
