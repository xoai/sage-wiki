import json

import httpx
import pytest

from sagewiki import AsyncSageWiki, SageWiki
from sagewiki.errors import InvalidArgument, SageWikiError


def make_client(handler, cls=SageWiki, **kwargs):
    kwargs.setdefault("url", "http://fixture:3333")
    kwargs.setdefault("token", "tok")
    c = cls(**kwargs)
    transport = httpx.MockTransport(handler)
    if cls is SageWiki:
        c._http = httpx.Client(transport=transport, timeout=kwargs.get("timeout", 30.0))
    else:
        c._http = httpx.AsyncClient(transport=transport, timeout=kwargs.get("timeout", 30.0))
    return c


class Capture:
    def __init__(self, response_json=None, status=200):
        self.requests = []
        self.response_json = response_json if response_json is not None else {}
        self.status = status

    def handler(self, request):
        self.requests.append(request)
        return httpx.Response(self.status, json=self.response_json)


# (method_name, args, kwargs, http_method, path, expected_params, expected_body)
METHOD_TABLE = [
    ("search", ("attention",), {}, "GET", "/v1/search",
     {"query": "attention", "limit": "10", "expand": "false", "rerank": "false"}, None),
    ("search", ("q",), {"tags": ["a", "b"], "channels": ["bm25", "vector"], "limit": 3, "expand": True},
     "GET", "/v1/search",
     {"query": "q", "tags": "a,b", "channels": "bm25,vector", "limit": "3", "expand": "true", "rerank": "false"}, None),
    ("read_article", ("concepts/x.md",), {}, "GET", "/v1/articles/concepts/x.md", {}, None),
    ("status", (), {}, "GET", "/v1/status", {}, None),
    ("list_entities", (), {"type": "concept"}, "GET", "/v1/entities", {"type": "concept"}, None),
    ("traverse", ("attention",), {"depth": 2, "direction": "both"},
     "GET", "/v1/ontology/attention/traverse", {"direction": "both", "depth": "2"}, None),
    ("graph_query", ("what is attention",), {"hops": 1, "mode": "local"},
     "POST", "/v1/graph/query", {},
     {"question": "what is attention", "hops": 1, "max_edges": 60, "mode": "local"}),
    ("provenance", (), {"article": "attention"}, "GET", "/v1/provenance", {"article": "attention"}, None),
    ("add_source", ("raw/x.md",), {}, "POST", "/v1/sources", {}, {"path": "raw/x.md"}),
    ("write_summary", ("s.md", "content"), {"concepts": ["a", "b"]},
     "PUT", "/v1/summaries", {}, {"source": "s.md", "content": "content", "concepts": "a,b"}),
    ("write_article", ("attention", "md"), {}, "PUT", "/v1/articles/attention", {}, {"content": "md"}),
    ("add_entity", ("e1", "concept", "Name"), {},
     "POST", "/v1/ontology/entities", {}, {"id": "e1", "type": "concept", "name": "Name"}),
    ("add_relation", ("a", "b", "relates_to"), {},
     "POST", "/v1/ontology/relations", {}, {"source_id": "a", "target_id": "b", "relation": "relates_to"}),
    ("learn", ("gotcha", "content"), {"tags": ["x"]},
     "POST", "/v1/learnings", {}, {"type": "gotcha", "content": "content", "tags": "x"}),
    ("capture", ("content",), {"context": "ctx", "tags": ["t1", "t2"]},
     "POST", "/v1/capture", {}, {"content": "content", "context": "ctx", "tags": "t1,t2"}),
    ("commit", (), {"message": "msg"}, "POST", "/v1/git/commit", {}, {"message": "msg"}),
    ("compile_diff", (), {}, "GET", "/v1/compile/diff", {}, None),
    ("compile", (), {"topic": "quantum", "max_sources": 5},
     "POST", "/v1/jobs/compile", {}, {"topic": "quantum", "max_sources": 5}),
    ("compile", (), {"dry_run": True},
     "POST", "/v1/jobs/compile", {}, {"dry_run": True, "fresh": False, "prune": False}),
    ("compile", (), {},
     "POST", "/v1/jobs/compile", {}, {"dry_run": False, "fresh": False, "prune": False}),
    ("lint", (), {"pass_": "connections", "fix": True},
     "POST", "/v1/jobs/lint", {}, {"pass": "connections", "fix": True}),
    ("job", ("id-1",), {}, "GET", "/v1/jobs/id-1", {}, None),
    ("jobs", (), {"status": "running"}, "GET", "/v1/jobs", {"status": "running"}, None),
    ("cancel_job", ("id-2",), {}, "DELETE", "/v1/jobs/id-2", {}, None),
]


@pytest.mark.parametrize(
    "name,args,kwargs,http_method,path,params,body", METHOD_TABLE,
    ids=[f"{m[0]}#{i}" for i, m in enumerate(METHOD_TABLE)],
)
def test_method_wire_shape(name, args, kwargs, http_method, path, params, body):
    cap = Capture({"job_id": "j", "kind": "compile", "status": "pending", "submitted_at": "t"})
    client = make_client(cap.handler)
    getattr(client, name)(*args, **kwargs)
    assert len(cap.requests) == 1
    req = cap.requests[0]
    assert req.method == http_method
    assert req.url.path == path
    for k, v in params.items():
        assert req.url.params[k] == v, f"param {k}: {req.url.params.get(k)} != {v}"
    if body is not None:
        assert json.loads(req.content) == body
    assert req.headers["authorization"] == "Bearer tok"


def test_idempotency_key_forwarded_verbatim():
    cap = Capture({})
    client = make_client(cap.handler)
    client.capture("c", idempotency_key="Key-123_ABC")
    assert cap.requests[0].headers["idempotency-key"] == "Key-123_ABC"


def test_no_token_sends_no_authorization_header():
    cap = Capture({})
    client = make_client(cap.handler, token=None)
    client.status()
    client.jobs()
    for req in cap.requests:
        assert "authorization" not in req.headers


def test_token_present_on_job_polls():
    cap = Capture({"job_id": "j", "kind": "compile", "status": "done", "submitted_at": "t"})
    client = make_client(cap.handler)
    client.job("j")
    client.jobs()
    for req in cap.requests:
        assert req.headers["authorization"] == "Bearer tok"


def test_compile_topic_mixed_with_flags_raises_locally():
    cap = Capture({})
    client = make_client(cap.handler)
    with pytest.raises(InvalidArgument):
        client.compile(topic="x", dry_run=True)
    assert cap.requests == []


def test_provenance_requires_exactly_one():
    cap = Capture({})
    client = make_client(cap.handler)
    with pytest.raises(InvalidArgument):
        client.provenance()
    with pytest.raises(InvalidArgument):
        client.provenance(source="a", article="b")
    assert cap.requests == []


def test_env_config_and_constructor_override(monkeypatch):
    monkeypatch.setenv("SAGE_WIKI_URL", "http://envhost:1")
    monkeypatch.setenv("SAGE_WIKI_TOKEN", "envtok")
    cap = Capture({})
    c = SageWiki()
    c._http = httpx.Client(transport=httpx.MockTransport(cap.handler))
    c.status()
    assert cap.requests[0].url.host == "envhost"
    assert cap.requests[0].headers["authorization"] == "Bearer envtok"
    cap2 = Capture({})
    c2 = make_client(cap2.handler)  # constructor wins over env
    c2.status()
    assert cap2.requests[0].url.host == "fixture"


def test_503_retried_up_to_retries():
    calls = {"n": 0}

    def handler(request):
        calls["n"] += 1
        if calls["n"] < 3:
            return httpx.Response(503, json={"error": {"code": "unavailable", "message": "down"}})
        return httpx.Response(200, json={"results": []})

    client = make_client(handler, retries=2)
    client.search("x")
    assert calls["n"] == 3


def test_non_idempotent_post_without_key_never_retried():
    calls = {"n": 0}

    def handler(request):
        calls["n"] += 1
        return httpx.Response(503, json={"error": {"code": "unavailable", "message": "down"}})

    client = make_client(handler, retries=3)
    with pytest.raises(SageWikiError):
        client.capture("c")
    assert calls["n"] == 1


def test_post_with_idempotency_key_is_retried():
    calls = {"n": 0}

    def handler(request):
        calls["n"] += 1
        if calls["n"] == 1:
            return httpx.Response(503, json={"error": {"code": "unavailable", "message": "down"}})
        return httpx.Response(200, json={"result": "ok"})

    client = make_client(handler, retries=2)
    client.capture("c", idempotency_key="k")
    assert calls["n"] == 2


def test_request_timeout_raises_not_hangs():
    # MockTransport can't simulate a hang (it runs synchronously), so this
    # uses a real socket that accepts and never responds.
    import socket
    import threading

    srv = socket.socket()
    srv.bind(("127.0.0.1", 0))
    srv.listen(1)
    port = srv.getsockname()[1]

    def stall():
        conn, _ = srv.accept()
        try:
            conn.recv(4096)
            threading.Event().wait(5)
        finally:
            conn.close()

    t = threading.Thread(target=stall, daemon=True)
    t.start()

    client = SageWiki(url=f"http://127.0.0.1:{port}", token=None, timeout=0.2)
    try:
        with pytest.raises(httpx.TimeoutException):
            client.status()
    finally:
        srv.close()


def test_configured_timeout_reaches_httpx():
    client = SageWiki(url="http://fixture:3333", timeout=7.5)
    assert client._http.timeout.read == 7.5


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "name,args,kwargs,http_method,path,params,body", METHOD_TABLE,
    ids=[f"async-{m[0]}#{i}" for i, m in enumerate(METHOD_TABLE)],
)
async def test_async_parity(name, args, kwargs, http_method, path, params, body):
    sync_cap = Capture({"job_id": "j", "kind": "compile", "status": "pending", "submitted_at": "t"})
    async_cap = Capture({"job_id": "j", "kind": "compile", "status": "pending", "submitted_at": "t"})
    sync_client = make_client(sync_cap.handler, SageWiki)
    async_client = make_client(async_cap.handler, AsyncSageWiki)
    getattr(sync_client, name)(*args, **kwargs)
    await getattr(async_client, name)(*args, **kwargs)
    s, a = sync_cap.requests[0], async_cap.requests[0]
    assert (s.method, str(s.url), s.content) == (a.method, str(a.url), a.content)
    assert s.headers == a.headers
