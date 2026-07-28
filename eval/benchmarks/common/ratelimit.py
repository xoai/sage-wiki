"""Process-wide rate-limit backpressure.

The 2026-07-28 parity run lost 1,011 questions because each worker met the
provider's 429 wall independently: the search subprocess degraded to
BM25-only, the harness retried twice with no delay, gave up, and the run
ground on for another 50 minutes marking questions failed.

This gate makes backoff *shared* — one worker's 429 pauses every worker —
and bounds it: if the wall persists past `give_up_after` seconds of
cumulative waiting with no successful call in between, it raises
QuotaExhausted so the run stops cleanly and can be resumed later, instead
of burning the remaining queue into infra_error records.
"""

from __future__ import annotations

import random
import threading
import time


class QuotaExhausted(Exception):
    """Sustained rate limiting — stop the run; it is resumable."""


class RateLimitGate:
    def __init__(self, base_delay: float = 5.0, max_delay: float = 120.0,
                 give_up_after: float = 900.0, sleep=time.sleep,
                 clock=time.monotonic, jitter=None):
        self.base_delay = base_delay
        self.max_delay = max_delay
        self.give_up_after = give_up_after
        self._sleep = sleep
        self._clock = clock
        self._jitter = jitter or (lambda: random.uniform(0, 0.5))
        self._lock = threading.Lock()
        self._cooldown_until = 0.0
        self._consecutive = 0
        self._streak_wait = 0.0     # cumulative wait since the last success
        self._total_wait = 0.0      # cumulative wait over the whole run
        self._events = 0

    def wait(self) -> None:
        """Block until any active cooldown expires. Call before each API use."""
        with self._lock:
            remaining = self._cooldown_until - self._clock()
        if remaining > 0:
            self._sleep(remaining)

    def report_limit(self, retry_after: float | None = None) -> float:
        """Record a 429/rate-limit event and open a cooldown. Returns its length.

        Raises QuotaExhausted when the streak's cumulative wait exceeds the
        budget — the caller should abort the run rather than keep failing.
        """
        with self._lock:
            self._consecutive += 1
            self._events += 1
            delay = min(self.max_delay, self.base_delay * (2 ** (self._consecutive - 1)))
            if retry_after is not None:
                delay = max(delay, float(retry_after))
            delay += self._jitter()
            self._streak_wait += delay
            self._total_wait += delay
            self._cooldown_until = max(self._cooldown_until, self._clock() + delay)
            over_budget = self._streak_wait > self.give_up_after
            streak, events = self._streak_wait, self._events
        if over_budget:
            raise QuotaExhausted(
                f"provider kept rate-limiting for {streak:.0f}s across {events} "
                f"events with no successful call — stopping the run. Completed "
                f"questions are checkpointed; rerun the same --project-name to "
                f"resume once quota is available."
            )
        return delay

    def report_success(self) -> None:
        """A call got through — reset escalation and the abort budget."""
        with self._lock:
            self._consecutive = 0
            self._streak_wait = 0.0

    def stats(self) -> dict:
        with self._lock:
            return {"rate_limit_events": self._events,
                    "total_wait_seconds": round(self._total_wait, 1)}


#: Shared by the LLM client and the sage-wiki search path so provider
#: pressure seen anywhere throttles everything.
GLOBAL_GATE = RateLimitGate()
