import inspect

import httpx
import pytest

from sagewiki import SageWiki
from sagewiki.errors import JobFailed, JobTimeout
from sagewiki.jobs import Job


def test_wait_timeout_has_no_default():
    sig = inspect.signature(Job.wait)
    assert sig.parameters["timeout"].default is inspect.Parameter.empty


JOB_PAYLOADS = {
    "pending": {"job_id": "j1", "kind": "compile", "status": "pending", "submitted_at": "t"},
    "running": {"job_id": "j1", "kind": "compile", "status": "running", "submitted_at": "t"},
    "done": {
        "job_id": "j1", "kind": "compile", "status": "done", "submitted_at": "t",
        "result": {"sources_compiled": 3},
    },
    "failed": {
        "job_id": "j1", "kind": "compile", "status": "failed", "submitted_at": "t",
        "error": {"code": "internal", "message": "LLM provider returned 429"},
    },
    "cancelled": {"job_id": "j1", "kind": "compile", "status": "cancelled", "submitted_at": "t"},
}


class JobServer:
    def __init__(self, sequence):
        self.sequence = list(sequence)
        self.calls = 0

    def handler(self, request):
        self.calls += 1
        payload = self.sequence[min(self.calls - 1, len(self.sequence) - 1)]
        return httpx.Response(200, json=JOB_PAYLOADS[payload])


def make_job(sequence):
    server = JobServer(sequence)
    client = SageWiki(url="http://fixture:3333", token="tok")
    client._http = httpx.Client(transport=httpx.MockTransport(server.handler))
    job = Job.from_dict(JOB_PAYLOADS["pending"], client)
    return job, server


def test_wait_polls_until_done():
    job, server = make_job(["running", "running", "done"])
    result = job.wait(timeout=10, poll_interval=0.01)
    assert result.status == "done"
    assert result.result == {"sources_compiled": 3}
    assert server.calls >= 3


def test_wait_timeout_raises():
    job, _ = make_job(["running"])
    with pytest.raises(JobTimeout):
        job.wait(timeout=0.05, poll_interval=0.01)


def test_wait_failed_raises_with_envelope():
    job, _ = make_job(["failed"])
    with pytest.raises(JobFailed) as exc_info:
        job.wait(timeout=5, poll_interval=0.01)
    assert exc_info.value.code == "internal"
    assert "429" in exc_info.value.message


def test_wait_cancelled_returns_without_raising():
    job, _ = make_job(["cancelled"])
    result = job.wait(timeout=5, poll_interval=0.01)
    assert result.status == "cancelled"


def test_refresh_mutates_and_returns_self():
    job, _ = make_job(["running"])
    out = job.refresh()
    assert out is job
    assert job.status == "running"
