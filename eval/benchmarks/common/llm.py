"""OpenAI chat-completions client for the benchmark harness.

Sync client + bounded ThreadPoolExecutor (spec §3): the pipeline is
subprocess-heavy, so threads compose naturally with subprocess.run. A
`transport` callable is injectable for offline tests; the real transport is
built lazily from the `openai` package so importing this module never
requires a key.
"""

from __future__ import annotations

import json
import re
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from benchmarks.common.ratelimit import GLOBAL_GATE, QuotaExhausted

MAX_RETRIES = 5
JSON_PARSE_RETRIES = 3
RETRYABLE_STATUS = {408, 409, 429, 500, 502, 503, 504}

_FENCE_RE = re.compile(r"^```(?:json)?\s*|\s*```$", re.MULTILINE)


class LLMError(Exception):
    """Raised when the LLM call fails permanently (retries exhausted or fatal)."""


def _retry_after(exc) -> float | None:
    """Seconds from a Retry-After header, when the provider sent one."""
    response = getattr(exc, "response", None)
    headers = getattr(response, "headers", None) or {}
    for key in ("retry-after", "Retry-After"):
        value = headers.get(key) if hasattr(headers, "get") else None
        if value is not None:
            try:
                return float(value)
            except (TypeError, ValueError):
                return None
    return None


def _real_transport(api_key: str | None = None):
    import openai  # type: ignore[import-not-found]  # deferred: tests never import it

    client = openai.OpenAI(api_key=api_key) if api_key else openai.OpenAI()

    def call(model, system, user, response_format=None):
        messages = []
        if system:
            messages.append({"role": "system", "content": system})
        messages.append({"role": "user", "content": user})
        kwargs = {"model": model, "messages": messages}
        if response_format:
            kwargs["response_format"] = response_format
        resp = client.chat.completions.create(**kwargs)
        content = resp.choices[0].message.content or ""
        usage = {
            "prompt_tokens": getattr(resp.usage, "prompt_tokens", 0) or 0,
            "completion_tokens": getattr(resp.usage, "completion_tokens", 0) or 0,
        }
        return content, usage

    return call


class LLMClient:
    def __init__(self, model: str, transport=None, workers: int = 8,
                 retry_base_delay: float = 2.0, role: str = "llm", gate=None):
        self.model = model
        self.transport = transport or _real_transport()
        self.workers = workers
        self.retry_base_delay = retry_base_delay
        self.role = role
        self.gate = gate if gate is not None else GLOBAL_GATE
        self._lock = threading.Lock()
        self._usage = {"prompt_tokens": 0, "completion_tokens": 0, "calls": 0}

    # -- core call with retries ------------------------------------------------

    def _call(self, system: str, user: str, response_format=None) -> str:
        last_exc = None
        for attempt in range(MAX_RETRIES):
            self.gate.wait()  # respect a cooldown another worker opened
            try:
                content, usage = self.transport(self.model, system, user,
                                                response_format=response_format)
                with self._lock:
                    self._usage["prompt_tokens"] += usage.get("prompt_tokens", 0)
                    self._usage["completion_tokens"] += usage.get("completion_tokens", 0)
                    self._usage["calls"] += 1
                self.gate.report_success()
                return content
            except QuotaExhausted:
                raise  # the run is over; never downgrade this to LLMError
            except Exception as exc:  # noqa: BLE001 — classified below
                last_exc = exc
                status = getattr(exc, "status_code", None)
                if status is not None and status not in RETRYABLE_STATUS:
                    raise LLMError(f"{self.role} call failed (status {status}): {exc}") from exc
                if status == 429:
                    # Shared backpressure: pause every worker, honor Retry-After.
                    self.gate.report_limit(retry_after=_retry_after(exc))
                elif attempt < MAX_RETRIES - 1:
                    time.sleep(self.retry_base_delay * (2 ** attempt))
        raise LLMError(f"{self.role} call failed after {MAX_RETRIES} attempts: {last_exc}") from last_exc

    # -- public API ------------------------------------------------------------

    def generate(self, system: str, user: str) -> str:
        return self._call(system, user)

    def generate_json(self, system: str, user: str) -> dict | None:
        """Return the parsed JSON object, or None after JSON_PARSE_RETRIES failures."""
        for _ in range(JSON_PARSE_RETRIES):
            raw = self._call(system, user, response_format={"type": "json_object"})
            text = _FENCE_RE.sub("", raw.strip()).strip()
            try:
                parsed = json.loads(text)
            except (json.JSONDecodeError, ValueError):
                continue
            if isinstance(parsed, dict):
                return parsed
        return None

    def map_generate(self, pairs: list[tuple[str, str]]) -> list[str]:
        """Run generate() over (system, user) pairs with bounded concurrency."""
        with ThreadPoolExecutor(max_workers=self.workers) as pool:
            return list(pool.map(lambda p: self.generate(*p), pairs))

    def usage(self) -> dict:
        with self._lock:
            return dict(self._usage)
