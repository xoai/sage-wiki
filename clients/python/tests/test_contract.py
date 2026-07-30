"""Contract test: the real client against a live sage-wiki server.

Run with the fixture:
    eval "$(scripts/p4-fixture-server.sh)"
    cd clients/python && pytest -q -m contract

Asserts the spec is truthful: every method gets a 2xx and a parsed model;
error probes map to the documented exception classes.
"""

import os

import pytest

from sagewiki import SageWiki
from sagewiki.errors import InvalidArgument, NotFound, Unauthenticated

pytestmark = [
    pytest.mark.contract,
    pytest.mark.skipif(not os.environ.get("SAGE_WIKI_URL"), reason="no live server (SAGE_WIKI_URL unset)"),
]


@pytest.fixture(scope="module")
def client():
    c = SageWiki()  # env config
    yield c
    c.close()


def test_status(client):
    s = client.status()
    assert s.project


def test_search_returns_seeded_result(client):
    r = client.search("attention", limit=3)
    assert len(r.results) >= 1
    assert any("attention" in i.content.lower() for i in r.results)


def test_read_article(client):
    a = client.read_article("concepts/attention.md")
    assert "Attention" in a.content


def test_list_entities(client):
    entities = client.list_entities(type="concept")
    assert any(e.id == "attention" for e in entities)


def test_traverse(client):
    client.traverse("attention", depth=1)


def test_graph_query(client):
    r = client.graph_query("what is attention", hops=1)
    assert isinstance(r.answer, str)


def test_provenance(client):
    client.provenance(article="attention")


def test_compile_diff(client):
    assert isinstance(client.compile_diff().diff, str)


def test_writes_roundtrip(client):
    r = client.write_summary("contract.md", "Contract test summary about recursion.", idempotency_key="contract-sum-1")
    assert r.result
    r = client.add_entity("recursion", "concept", "Recursion", idempotency_key="contract-ent-1")
    assert r.result
    r = client.capture("contract capture about memoization", idempotency_key="contract-cap-1")
    assert r.result


def test_capture_idempotent_replay(client):
    r1 = client.capture("idempotent contract capture", idempotency_key="contract-cap-idem")
    r2 = client.capture("idempotent contract capture", idempotency_key="contract-cap-idem")
    assert r1.result == r2.result


def test_commit(client):
    client.commit(message="contract test commit")


def test_job_flow_lint(client):
    job = client.lint(fix=False)
    assert job.job_id
    done = job.wait(timeout=120, poll_interval=0.5)
    assert done.status in ("done", "failed")  # lint may fail without LLM; flow is what matters
    fetched = client.job(job.job_id)
    assert fetched.job_id == job.job_id
    jobs = client.jobs()
    assert any(j.job_id == job.job_id for j in jobs)


def test_compile_dry_run_job(client):
    job = client.compile(dry_run=True)
    done = job.wait(timeout=120, poll_interval=0.5)
    assert done.status in ("done", "failed")


def test_error_unknown_job_is_not_found(client):
    with pytest.raises(NotFound):
        client.job("00000000-0000-4000-8000-000000000000")


def test_error_mixed_compile_body_is_invalid_argument(client):
    import httpx

    resp = client._http.post(
        f"{client.base_url}/v1/jobs/compile",
        headers={"Authorization": f"Bearer {client.token}", "Content-Type": "application/json"},
        json={"topic": "x", "dry_run": True},
    )
    from sagewiki.errors import raise_for_envelope

    assert resp.status_code == 400
    with pytest.raises(InvalidArgument):
        raise_for_envelope(resp.status_code, resp.json())


def test_error_bad_token_is_unauthenticated():
    c = SageWiki(url=os.environ["SAGE_WIKI_URL"], token="wrong-token")
    try:
        with pytest.raises(Unauthenticated):
            c.status()
    finally:
        c.close()
