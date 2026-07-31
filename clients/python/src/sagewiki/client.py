"""Synchronous and asynchronous clients for sage-wiki's /v1 API.

One request-building code path (`_BaseClient._prepare`) feeds both the sync
and async executors — parity is enforced by test, not by convention.
"""

from __future__ import annotations

import os
import time
from typing import TYPE_CHECKING, Any, Dict, List, Optional
from urllib.parse import quote

import httpx

if TYPE_CHECKING:
    from sagewiki.jobs import AsyncJob, Job

from sagewiki.errors import InvalidArgument, SageWikiError, raise_for_envelope
from sagewiki.models import (
    Article,
    CompileDiff,
    Entity,
    GraphQueryResult,
    ProvenanceResult,
    SearchResults,
    Status,
    TextResult,
    TraverseResult,
)

_WRITE_METHODS = {"POST", "PUT", "DELETE"}


class _BaseClient:
    def __init__(
        self,
        url: Optional[str] = None,
        token: Optional[str] = None,
        retries: int = 0,
        timeout: float = 30.0,
    ):
        self.base_url = (url or os.environ.get("SAGE_WIKI_URL") or "http://127.0.0.1:3333").rstrip("/")
        self.token = token if token is not None else os.environ.get("SAGE_WIKI_TOKEN") or None
        self.retries = retries
        self.timeout = timeout

    def _prepare(
        self,
        method: str,
        path: str,
        params: Optional[Dict[str, Any]] = None,
        body: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
    ) -> Dict[str, Any]:
        headers = {"Content-Type": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if idempotency_key is not None:
            headers["Idempotency-Key"] = idempotency_key
        clean_params = {k: v for k, v in (params or {}).items() if v is not None}
        return {
            "method": method,
            "url": self.base_url + path,
            "params": clean_params,
            "json": body,
            "headers": headers,
        }

    def _attempts(self, method: str, idempotency_key: Optional[str]) -> int:
        # Never auto-retry a non-idempotent write without an Idempotency-Key.
        if method in _WRITE_METHODS and idempotency_key is None:
            return 1
        return 1 + max(0, self.retries)

    @staticmethod
    def _backoff(attempt: int) -> float:
        return min(0.1 * (2 ** attempt), 2.0)


class SageWiki(_BaseClient):
    """Synchronous client."""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self._http = httpx.Client(timeout=self.timeout)

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> "SageWiki":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def _call(
        self,
        method: str,
        path: str,
        params: Optional[Dict[str, Any]] = None,
        body: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        req = self._prepare(method, path, params, body, idempotency_key)
        attempts = self._attempts(method, idempotency_key)
        last_exc: Optional[Exception] = None
        for attempt in range(attempts):
            try:
                resp = self._http.request(**req)
            except (httpx.ConnectError, httpx.ConnectTimeout) as e:
                last_exc = e
                if attempt + 1 < attempts:
                    time.sleep(self._backoff(attempt))
                    continue
                raise
            if resp.status_code == 503 and attempt + 1 < attempts:
                time.sleep(self._backoff(attempt))
                continue
            if resp.status_code >= 400:
                raise_for_envelope(resp.status_code, _safe_json(resp))
            if resp.status_code == 204 or not resp.content:
                return {}
            return _success_json(resp)
        raise SageWikiError("unavailable", "request failed after retries") from last_exc

    # -- read ----------------------------------------------------------
    def search(
        self,
        query: str,
        *,
        tags: Optional[List[str]] = None,
        boost_tags: Optional[List[str]] = None,
        limit: int = 10,
        channels: Optional[List[str]] = None,
        expand: bool = False,
        rerank: bool = False,
    ) -> SearchResults:
        data = self._call("GET", "/v1/search", params={
            "query": query,
            "tags": ",".join(tags) if tags else None,
            "boost_tags": ",".join(boost_tags) if boost_tags else None,
            "limit": limit,
            "channels": ",".join(channels) if channels else None,
            "expand": str(expand).lower(),
            "rerank": str(rerank).lower(),
        })
        return SearchResults.from_dict(data)

    def read_article(self, path: str) -> Article:
        return Article.from_dict(self._call("GET", f"/v1/articles/{quote(path, safe='/')}"))

    def status(self) -> Status:
        return Status.from_dict(self._call("GET", "/v1/status"))

    def list_entities(self, type: Optional[str] = None) -> List[Entity]:
        data = self._call("GET", "/v1/entities", params={"type": type})
        return [Entity.from_dict(e) for e in data.get("entities") or []]

    def traverse(
        self,
        entity: str,
        *,
        relation: Optional[str] = None,
        direction: str = "outbound",
        depth: int = 1,
    ) -> TraverseResult:
        data = self._call("GET", f"/v1/ontology/{quote(entity, safe='')}/traverse",
                          params={"relation": relation, "direction": direction, "depth": depth})
        return TraverseResult.from_dict(data)

    def graph_query(
        self,
        question: str,
        *,
        hops: int = 2,
        max_edges: int = 60,
        as_of: Optional[str] = None,
        mode: str = "local",
    ) -> GraphQueryResult:
        body = {"question": question, "hops": hops, "max_edges": max_edges, "mode": mode}
        if as_of is not None:
            body["as_of"] = as_of
        return GraphQueryResult.from_dict(self._call("POST", "/v1/graph/query", body=body))

    def provenance(self, *, source: Optional[str] = None, article: Optional[str] = None) -> ProvenanceResult:
        # Empty strings are as absent as None — validate before any HTTP call.
        source = source or None
        article = article or None
        if (source is None) == (article is None):
            raise InvalidArgument("invalid_argument", "exactly one of source or article is required")
        data = self._call("GET", "/v1/provenance", params={"source": source, "article": article})
        return ProvenanceResult.from_dict(data)

    def compile_diff(self) -> CompileDiff:
        return CompileDiff.from_dict(self._call("GET", "/v1/compile/diff"))

    # -- write ---------------------------------------------------------
    def add_source(self, path: str, *, type: Optional[str] = None, idempotency_key: Optional[str] = None) -> TextResult:
        body = {"path": path}
        if type is not None:
            body["type"] = type
        return TextResult.from_dict(self._call("POST", "/v1/sources", body=body, idempotency_key=idempotency_key))

    def write_summary(
        self,
        source: str,
        content: str,
        *,
        concepts: Optional[List[str]] = None,
        idempotency_key: Optional[str] = None,
    ) -> TextResult:
        body = {"source": source, "content": content}
        if concepts is not None:
            body["concepts"] = ",".join(concepts)
        return TextResult.from_dict(self._call("PUT", "/v1/summaries", body=body, idempotency_key=idempotency_key))

    def write_article(self, concept: str, content: str, *, idempotency_key: Optional[str] = None) -> TextResult:
        return TextResult.from_dict(
            self._call("PUT", f"/v1/articles/{quote(concept, safe='')}", body={"content": content}, idempotency_key=idempotency_key)
        )

    def add_entity(self, id: str, type: str, name: str, *, idempotency_key: Optional[str] = None) -> TextResult:
        return TextResult.from_dict(self._call(
            "POST", "/v1/ontology/entities",
            body={"id": id, "type": type, "name": name}, idempotency_key=idempotency_key))

    def add_relation(self, source_id: str, target_id: str, relation: str, *, idempotency_key: Optional[str] = None) -> TextResult:
        return TextResult.from_dict(self._call(
            "POST", "/v1/ontology/relations",
            body={"source_id": source_id, "target_id": target_id, "relation": relation},
            idempotency_key=idempotency_key))

    def learn(self, type: str, content: str, *, tags: Optional[List[str]] = None, idempotency_key: Optional[str] = None) -> TextResult:
        body = {"type": type, "content": content}
        if tags is not None:
            body["tags"] = ",".join(tags)
        return TextResult.from_dict(self._call("POST", "/v1/learnings", body=body, idempotency_key=idempotency_key))

    def capture(
        self,
        content: str,
        *,
        context: Optional[str] = None,
        tags: Optional[List[str]] = None,
        idempotency_key: Optional[str] = None,
    ) -> TextResult:
        body = {"content": content}
        if context is not None:
            body["context"] = context
        if tags is not None:
            body["tags"] = ",".join(tags)
        return TextResult.from_dict(self._call("POST", "/v1/capture", body=body, idempotency_key=idempotency_key))

    def commit(self, message: Optional[str] = None) -> TextResult:
        body = {}
        if message is not None:
            body["message"] = message
        return TextResult.from_dict(self._call("POST", "/v1/git/commit", body=body))

    # -- jobs ----------------------------------------------------------
    def compile(
        self,
        *,
        topic: Optional[str] = None,
        max_sources: Optional[int] = None,
        dry_run: bool = False,
        fresh: bool = False,
        prune: bool = False,
        idempotency_key: Optional[str] = None,
    ):
        from sagewiki.jobs import Job

        if topic is not None:
            if dry_run or fresh or prune:
                raise InvalidArgument(
                    "invalid_argument", "exactly one of 'topic' or compile flags expected")
            body: Dict[str, Any] = {"topic": topic}
            if max_sources is not None:
                body["max_sources"] = max_sources
        else:
            # The server 400s a flag-less body — always serialize dry_run.
            body = {"dry_run": dry_run, "fresh": fresh, "prune": prune}
        data = self._call("POST", "/v1/jobs/compile", body=body, idempotency_key=idempotency_key)
        return Job.from_dict(data, self)

    def lint(self, *, pass_: Optional[str] = None, fix: bool = False, idempotency_key: Optional[str] = None):
        from sagewiki.jobs import Job

        body: Dict[str, Any] = {"fix": fix}
        if pass_ is not None:
            body["pass"] = pass_
        data = self._call("POST", "/v1/jobs/lint", body=body, idempotency_key=idempotency_key)
        return Job.from_dict(data, self)

    def job(self, job_id: str):
        from sagewiki.jobs import Job

        return Job.from_dict(self._call("GET", f"/v1/jobs/{quote(job_id, safe='')}"), self)

    def jobs(self, *, status: Optional[str] = None) -> List["Job"]:
        from sagewiki.jobs import Job

        data = self._call("GET", "/v1/jobs", params={"status": status})
        return [Job.from_dict(j, self) for j in data.get("jobs") or []]

    def cancel_job(self, job_id: str):
        from sagewiki.jobs import Job

        return Job.from_dict(self._call("DELETE", f"/v1/jobs/{quote(job_id, safe='')}"), self)


class AsyncSageWiki(_BaseClient):
    """Asynchronous client — identical requests, async execution."""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self._http = httpx.AsyncClient(timeout=self.timeout)

    async def close(self) -> None:
        await self._http.aclose()

    async def __aenter__(self) -> "AsyncSageWiki":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    async def _call(
        self,
        method: str,
        path: str,
        params: Optional[Dict[str, Any]] = None,
        body: Optional[Dict[str, Any]] = None,
        idempotency_key: Optional[str] = None,
    ) -> Any:
        import asyncio

        req = self._prepare(method, path, params, body, idempotency_key)
        attempts = self._attempts(method, idempotency_key)
        last_exc: Optional[Exception] = None
        for attempt in range(attempts):
            try:
                resp = await self._http.request(**req)
            except (httpx.ConnectError, httpx.ConnectTimeout) as e:
                last_exc = e
                if attempt + 1 < attempts:
                    await asyncio.sleep(self._backoff(attempt))
                    continue
                raise
            if resp.status_code == 503 and attempt + 1 < attempts:
                await asyncio.sleep(self._backoff(attempt))
                continue
            if resp.status_code >= 400:
                raise_for_envelope(resp.status_code, _safe_json(resp))
            if resp.status_code == 204 or not resp.content:
                return {}
            return _success_json(resp)
        raise SageWikiError("unavailable", "request failed after retries") from last_exc

    async def search(self, query: str, *, tags=None, boost_tags=None, limit=10, channels=None,
                     expand=False, rerank=False) -> SearchResults:
        data = await self._call("GET", "/v1/search", params={
            "query": query,
            "tags": ",".join(tags) if tags else None,
            "boost_tags": ",".join(boost_tags) if boost_tags else None,
            "limit": limit,
            "channels": ",".join(channels) if channels else None,
            "expand": str(expand).lower(),
            "rerank": str(rerank).lower(),
        })
        return SearchResults.from_dict(data)

    async def read_article(self, path: str) -> Article:
        return Article.from_dict(await self._call("GET", f"/v1/articles/{quote(path, safe='/')}"))

    async def status(self) -> Status:
        return Status.from_dict(await self._call("GET", "/v1/status"))

    async def list_entities(self, type: Optional[str] = None) -> List[Entity]:
        data = await self._call("GET", "/v1/entities", params={"type": type})
        return [Entity.from_dict(e) for e in data.get("entities") or []]

    async def traverse(self, entity: str, *, relation=None, direction="outbound", depth=1) -> TraverseResult:
        data = await self._call("GET", f"/v1/ontology/{quote(entity, safe='')}/traverse",
                                params={"relation": relation, "direction": direction, "depth": depth})
        return TraverseResult.from_dict(data)

    async def graph_query(self, question: str, *, hops=2, max_edges=60, as_of=None, mode="local") -> GraphQueryResult:
        body = {"question": question, "hops": hops, "max_edges": max_edges, "mode": mode}
        if as_of is not None:
            body["as_of"] = as_of
        return GraphQueryResult.from_dict(await self._call("POST", "/v1/graph/query", body=body))

    async def provenance(self, *, source=None, article=None) -> ProvenanceResult:
        source = source or None
        article = article or None
        if (source is None) == (article is None):
            raise InvalidArgument("invalid_argument", "exactly one of source or article is required")
        data = await self._call("GET", "/v1/provenance", params={"source": source, "article": article})
        return ProvenanceResult.from_dict(data)

    async def compile_diff(self) -> CompileDiff:
        return CompileDiff.from_dict(await self._call("GET", "/v1/compile/diff"))

    async def add_source(self, path: str, *, type=None, idempotency_key=None) -> TextResult:
        body = {"path": path}
        if type is not None:
            body["type"] = type
        return TextResult.from_dict(await self._call("POST", "/v1/sources", body=body, idempotency_key=idempotency_key))

    async def write_summary(self, source: str, content: str, *, concepts=None, idempotency_key=None) -> TextResult:
        body = {"source": source, "content": content}
        if concepts is not None:
            body["concepts"] = ",".join(concepts)
        return TextResult.from_dict(await self._call("PUT", "/v1/summaries", body=body, idempotency_key=idempotency_key))

    async def write_article(self, concept: str, content: str, *, idempotency_key=None) -> TextResult:
        return TextResult.from_dict(await self._call(
            "PUT", f"/v1/articles/{quote(concept, safe='')}", body={"content": content}, idempotency_key=idempotency_key))

    async def add_entity(self, id: str, type: str, name: str, *, idempotency_key=None) -> TextResult:
        return TextResult.from_dict(await self._call(
            "POST", "/v1/ontology/entities",
            body={"id": id, "type": type, "name": name}, idempotency_key=idempotency_key))

    async def add_relation(self, source_id: str, target_id: str, relation: str, *, idempotency_key=None) -> TextResult:
        return TextResult.from_dict(await self._call(
            "POST", "/v1/ontology/relations",
            body={"source_id": source_id, "target_id": target_id, "relation": relation},
            idempotency_key=idempotency_key))

    async def learn(self, type: str, content: str, *, tags=None, idempotency_key=None) -> TextResult:
        body = {"type": type, "content": content}
        if tags is not None:
            body["tags"] = ",".join(tags)
        return TextResult.from_dict(await self._call("POST", "/v1/learnings", body=body, idempotency_key=idempotency_key))

    async def capture(self, content: str, *, context=None, tags=None, idempotency_key=None) -> TextResult:
        body = {"content": content}
        if context is not None:
            body["context"] = context
        if tags is not None:
            body["tags"] = ",".join(tags)
        return TextResult.from_dict(await self._call("POST", "/v1/capture", body=body, idempotency_key=idempotency_key))

    async def commit(self, message: Optional[str] = None) -> TextResult:
        body = {}
        if message is not None:
            body["message"] = message
        return TextResult.from_dict(await self._call("POST", "/v1/git/commit", body=body))

    async def compile(self, *, topic=None, max_sources=None, dry_run=False, fresh=False,
                      prune=False, idempotency_key=None):
        from sagewiki.jobs import AsyncJob

        if topic is not None:
            if dry_run or fresh or prune:
                raise InvalidArgument(
                    "invalid_argument", "exactly one of 'topic' or compile flags expected")
            body: Dict[str, Any] = {"topic": topic}
            if max_sources is not None:
                body["max_sources"] = max_sources
        else:
            body = {"dry_run": dry_run, "fresh": fresh, "prune": prune}
        data = await self._call("POST", "/v1/jobs/compile", body=body, idempotency_key=idempotency_key)
        return AsyncJob.from_dict(data, self)

    async def lint(self, *, pass_=None, fix=False, idempotency_key=None):
        from sagewiki.jobs import AsyncJob

        body: Dict[str, Any] = {"fix": fix}
        if pass_ is not None:
            body["pass"] = pass_
        data = await self._call("POST", "/v1/jobs/lint", body=body, idempotency_key=idempotency_key)
        return AsyncJob.from_dict(data, self)

    async def job(self, job_id: str):
        from sagewiki.jobs import AsyncJob

        return AsyncJob.from_dict(await self._call("GET", f"/v1/jobs/{quote(job_id, safe='')}"), self)

    async def jobs(self, *, status: Optional[str] = None) -> List["AsyncJob"]:
        from sagewiki.jobs import AsyncJob

        data = await self._call("GET", "/v1/jobs", params={"status": status})
        return [AsyncJob.from_dict(j, self) for j in data.get("jobs") or []]

    async def cancel_job(self, job_id: str):
        from sagewiki.jobs import AsyncJob

        return AsyncJob.from_dict(await self._call("DELETE", f"/v1/jobs/{quote(job_id, safe='')}"), self)


def _safe_json(resp: httpx.Response) -> Any:
    try:
        return resp.json()
    except ValueError:
        return resp.text


def _success_json(resp: httpx.Response) -> Any:
    """Parse a 2xx body; fail loudly on non-JSON (proxy HTML, redirect
    targets) instead of feeding a string to model parsers."""
    parsed = _safe_json(resp)
    if isinstance(parsed, str):
        raise SageWikiError(f"http_{resp.status_code}", f"HTTP {resp.status_code} with non-JSON body")
    return parsed
