"""Async job handles for the sage-wiki /v1 jobs API.

``wait`` requires an explicit timeout — an unbounded wait is impossible to
write by accident. Cancellation is not failure: a cancelled job returns from
``wait`` without raising.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from typing import Any, Dict, List, Optional

from sagewiki.errors import JobFailed, JobTimeout, SageWikiError

_TERMINAL = {"done", "failed", "cancelled"}


@dataclass
class Job:
    job_id: str = ""
    kind: str = ""
    status: str = ""
    submitted_at: Optional[str] = None
    started_at: Optional[str] = None
    finished_at: Optional[str] = None
    progress: Any = None
    result: Any = None
    error: Optional[Dict[str, Any]] = None
    _client: Any = None

    @classmethod
    def from_dict(cls, d: Dict[str, Any], client: Any = None) -> "Job":
        return cls(
            job_id=str(d.get("job_id", "")),
            kind=str(d.get("kind", "")),
            status=str(d.get("status", "")),
            submitted_at=d.get("submitted_at"),
            started_at=d.get("started_at"),
            finished_at=d.get("finished_at"),
            progress=d.get("progress"),
            result=d.get("result"),
            error=d.get("error"),
            _client=client,
        )

    @property
    def terminal(self) -> bool:
        return self.status in _TERMINAL

    def _update(self, other: "Job") -> None:
        self.status = other.status
        self.submitted_at = other.submitted_at
        self.started_at = other.started_at
        self.finished_at = other.finished_at
        self.progress = other.progress
        self.result = other.result
        self.error = other.error

    def _evaluate(self) -> Optional["Job"]:
        """Terminal-state decision shared by both wait loops: raise on
        failed, return self on terminal, None to keep polling."""
        if self.status == "failed":
            env = self.error or {}
            raise JobFailed(
                str(env.get("code", "internal")),
                str(env.get("message", "job failed")),
                env.get("details"),
            )
        if self.terminal:
            return self
        return None

    def refresh(self) -> "Job":
        if self._client is None:
            raise SageWikiError(
                "internal", "job is not bound to a client — obtain jobs via client.job()/compile()/lint()")
        self._update(self._client.job(self.job_id))
        return self

    def wait(self, timeout: float, *, poll_interval: float = 1.0) -> "Job":
        """Poll until a terminal state. Raises JobTimeout on expiry,
        JobFailed (carrying the error envelope) on failure. Cancelled
        returns the job without raising — cancellation is not failure."""
        deadline = time.monotonic() + timeout
        while True:
            self.refresh()
            done = self._evaluate()
            if done is not None:
                return done
            if time.monotonic() >= deadline:
                raise JobTimeout("timeout", f"job {self.job_id} not terminal after {timeout}s")
            time.sleep(min(poll_interval, max(0.0, deadline - time.monotonic())))


@dataclass
class AsyncJob(Job):
    @classmethod
    def from_dict(cls, d: Dict[str, Any], client: Any = None) -> "AsyncJob":
        base = Job.from_dict(d, client)
        return cls(**{k: getattr(base, k) for k in base.__dataclass_fields__})

    async def refresh(self) -> "AsyncJob":
        if self._client is None:
            raise SageWikiError(
                "internal", "job is not bound to a client — obtain jobs via client.job()/compile()/lint()")
        self._update(await self._client.job(self.job_id))
        return self

    async def wait(self, timeout: float, *, poll_interval: float = 1.0) -> "AsyncJob":
        deadline = time.monotonic() + timeout
        while True:
            await self.refresh()
            done = self._evaluate()
            if done is not None:
                return self
            if time.monotonic() >= deadline:
                raise JobTimeout("timeout", f"job {self.job_id} not terminal after {timeout}s")
            await asyncio.sleep(min(poll_interval, max(0.0, deadline - time.monotonic())))
