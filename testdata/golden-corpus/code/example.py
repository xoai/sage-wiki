"""Rate limiter implementing the token bucket algorithm."""

import time


class TokenBucket:
    """Token bucket with configurable rate and burst capacity."""

    def __init__(self, rate_per_sec: float, burst: int):
        self.rate = rate_per_sec
        self.burst = burst
        self.tokens = float(burst)
        self.updated = time.monotonic()

    def allow(self, cost: float = 1.0) -> bool:
        """Consume `cost` tokens if available, refilling by elapsed time."""
        now = time.monotonic()
        self.tokens = min(self.burst, self.tokens + (now - self.updated) * self.rate)
        self.updated = now
        if self.tokens >= cost:
            self.tokens -= cost
            return True
        return False
