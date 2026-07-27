"""T2 — LLMClient: retries, parse-fail fallback, usage accounting, concurrency bound."""

import json
import threading
import time

import pytest

from benchmarks.common.llm import LLMClient, LLMError


class FakeTransport:
    """Callable standing in for the OpenAI chat-completions call.

    Scripted responses: each entry is either a (content, usage) tuple or an
    Exception instance to raise.
    """

    def __init__(self, script):
        self.script = list(script)
        self.calls = []
        self.lock = threading.Lock()
        self.in_flight = 0
        self.max_in_flight = 0

    def __call__(self, model, system, user, response_format=None):
        with self.lock:
            self.in_flight += 1
            self.max_in_flight = max(self.max_in_flight, self.in_flight)
            self.calls.append({"model": model, "system": system, "user": user,
                               "response_format": response_format})
            step = self.script.pop(0) if self.script else self.script_default
        try:
            time.sleep(0.01)
            if isinstance(step, Exception):
                raise step
            return step
        finally:
            with self.lock:
                self.in_flight -= 1

    script_default = ("ok", {"prompt_tokens": 1, "completion_tokens": 1})


class Retryable(Exception):
    status_code = 429


def make_client(script, **kw):
    t = FakeTransport(script)
    c = LLMClient(model="test-model", transport=t, retry_base_delay=0.001, **kw)
    return c, t


class TestGenerate:
    def test_returns_content_and_counts_usage(self):
        c, t = make_client([("hello", {"prompt_tokens": 10, "completion_tokens": 5})])
        assert c.generate("sys", "user") == "hello"
        u = c.usage()
        assert u["prompt_tokens"] == 10 and u["completion_tokens"] == 5 and u["calls"] == 1

    def test_retries_on_429_then_succeeds(self):
        c, t = make_client([Retryable(), Retryable(), ("ok", {"prompt_tokens": 1, "completion_tokens": 1})])
        assert c.generate("s", "u") == "ok"
        assert len(t.calls) == 3

    def test_raises_after_max_retries(self):
        c, t = make_client([Retryable()] * 10)
        with pytest.raises(LLMError):
            c.generate("s", "u")
        assert len(t.calls) == 5  # max_retries

    def test_non_retryable_error_propagates_immediately(self):
        class Fatal(Exception):
            status_code = 400
        c, t = make_client([Fatal("bad request")])
        with pytest.raises(LLMError):
            c.generate("s", "u")
        assert len(t.calls) == 1


class TestGenerateJson:
    def test_parses_json_object(self):
        c, t = make_client([(json.dumps({"label": "CORRECT"}), {"prompt_tokens": 1, "completion_tokens": 1})])
        assert c.generate_json("s", "u") == {"label": "CORRECT"}
        assert t.calls[0]["response_format"] == {"type": "json_object"}

    def test_strips_code_fences(self):
        c, t = make_client([("```json\n{\"a\": 1}\n```", {"prompt_tokens": 1, "completion_tokens": 1})])
        assert c.generate_json("s", "u") == {"a": 1}

    def test_parse_retry_then_none(self):
        c, t = make_client([("not json", {"prompt_tokens": 1, "completion_tokens": 1})] * 3)
        assert c.generate_json("s", "u") is None
        assert len(t.calls) == 3  # json_parse_retries

    def test_parse_retry_recovers(self):
        c, t = make_client([
            ("nope", {"prompt_tokens": 1, "completion_tokens": 1}),
            ("{\"ok\": true}", {"prompt_tokens": 1, "completion_tokens": 1}),
        ])
        assert c.generate_json("s", "u") == {"ok": True}


class TestConcurrencyBound:
    def test_in_flight_never_exceeds_workers(self):
        c, t = make_client([], workers=3)
        results = c.map_generate([("s", f"u{i}") for i in range(12)])
        assert len(results) == 12
        assert t.max_in_flight <= 3

    def test_usage_accumulates_across_threads(self):
        c, t = make_client([], workers=4)
        c.map_generate([("s", f"u{i}") for i in range(8)])
        assert c.usage()["calls"] == 8
        assert c.usage()["prompt_tokens"] == 8
