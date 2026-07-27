"""T3 — SageWikiBackend against a scripted fake sage-wiki binary."""

import json
import os
import stat
from pathlib import Path

import pytest

from benchmarks.common.sagewiki import (
    CompileError,
    DegradedSearchError,
    SageWikiBackend,
    default_binary,
    require_api_key,
)

FAKE = Path(__file__).parent / "fake_sage_wiki.sh"


@pytest.fixture()
def backend(tmp_path):
    return SageWikiBackend(binary=str(FAKE), root=tmp_path / "projects")


def set_fixture(tmp_path, monkeypatch, name):
    """Tell the fake binary which canned behavior to use."""
    monkeypatch.setenv("FAKE_SW_MODE", name)
    monkeypatch.setenv("FAKE_SW_STATE", str(tmp_path / "fake-state"))


SEARCH_HYBRID = {"ok": True, "data": [
    {"ID": "m1", "Content": "Alice adopted Biscuit.", "Tags": None,
     "ArticlePath": "wiki/concepts/biscuit.md", "BM25Rank": 1, "VectorRank": 2, "RRFScore": 0.03},
    {"ID": "m2", "Content": "Bob moved to Lisbon.", "Tags": None,
     "ArticlePath": "wiki/concepts/lisbon.md", "BM25Rank": 2, "VectorRank": 0, "RRFScore": 0.01},
]}
SEARCH_BM25_ONLY = {"ok": True, "data": [
    {"ID": "m1", "Content": "x", "Tags": None, "ArticlePath": "a.md",
     "BM25Rank": 1, "VectorRank": 0, "RRFScore": 0.02},
]}


class TestProjectSetup:
    def test_init_project_renders_config_without_key_material(self, backend):
        proj = backend.init_project("conv0")
        cfg = (proj / "config.yaml").read_text()
        assert "version: 1" in cfg and "project: conv0" in cfg
        assert "api_key: ${OPENAI_API_KEY}" in cfg
        assert "rate_limit: 120" in cfg
        assert cfg.count("gpt-4o-mini") == 5  # summarize/extract/write/lint/query
        assert "text-embedding-3-small" in cfg
        assert "auto_commit: false" in cfg and "auto_lint: false" in cfg
        assert "sk-" not in cfg
        assert (proj / "raw").is_dir()

    def test_write_session(self, backend):
        backend.init_project("conv0")
        p = backend.write_session("conv0", "session_001", "# Session 1\n\nhello\n")
        assert p.read_text().startswith("# Session 1")
        assert p.parent.name == "raw"


class TestCompile:
    def test_compile_success_writes_marker_and_skips_on_resume(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "ok")
        backend.init_project("conv0")
        backend.write_session("conv0", "s1", "hi")
        stats = backend.compile("conv0")
        assert stats.skipped is False and stats.vector_count == 42
        stats2 = backend.compile("conv0")
        assert stats2.skipped is True
        compile_calls = (tmp_path / "fake-state" / "compile-calls").read_text()
        assert compile_calls.strip() == "1"  # resume did not re-invoke compile

    def test_compile_recompiles_when_sources_change(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "ok")
        backend.init_project("conv0")
        backend.write_session("conv0", "s1", "hi")
        backend.compile("conv0")
        backend.write_session("conv0", "s2", "more")
        assert backend.compile("conv0").skipped is False

    def test_compile_failure_retries_once_then_raises(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "compile-fail")
        backend.init_project("conv0")
        backend.write_session("conv0", "s1", "hi")
        with pytest.raises(CompileError):
            backend.compile("conv0")
        state = (tmp_path / "fake-state" / "compile-calls").read_text()
        assert state.strip() == "2"  # first attempt + one retry
        log = backend.project_dir("conv0") / "compile.log"
        assert "boom" in log.read_text()

    def test_compile_zero_vectors_fails(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "no-vectors")
        backend.init_project("conv0")
        backend.write_session("conv0", "s1", "hi")
        with pytest.raises(CompileError, match="vector"):
            backend.compile("conv0")


class TestSearch:
    def test_search_parses_results_and_latency(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "ok")
        backend.init_project("conv0")
        resp = backend.search("conv0", "who adopted a dog?", limit=10)
        assert [r["memory"] for r in resp.results] == ["Alice adopted Biscuit.", "Bob moved to Lisbon."]
        assert resp.results[0]["score"] == pytest.approx(0.03)
        assert resp.results[0]["id"] == "m1"
        assert resp.latency_ms >= 0

    def test_search_zero_hit_null_data(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "null-data")
        backend.init_project("conv0")
        resp = backend.search("conv0", "nothing", limit=10)
        assert resp.results == []

    def test_search_degraded_by_stderr_warning(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "embed-warning")
        backend.init_project("conv0")
        with pytest.raises(DegradedSearchError):
            backend.search("conv0", "q", limit=10)
        calls = (tmp_path / "fake-state" / "search-calls").read_text()
        assert calls.strip() == "3"  # initial + 2 retries

    def test_search_degraded_by_shape_guard_all_bm25(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "bm25-only")
        backend.init_project("conv0")
        with pytest.raises(DegradedSearchError):
            backend.search("conv0", "q", limit=10)

    def test_search_recovers_when_degrade_is_transient(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "embed-warning-once")
        backend.init_project("conv0")
        resp = backend.search("conv0", "q", limit=10)
        assert len(resp.results) == 2


class TestFailFast:
    def test_missing_binary_mentions_go_build(self, tmp_path):
        b = SageWikiBackend(binary=str(tmp_path / "nope"), root=tmp_path / "p")
        with pytest.raises(FileNotFoundError, match="go build"):
            b.init_project("x")

    def test_default_binary_is_repo_root_anchored(self):
        assert default_binary() == str(
            Path(__file__).resolve().parents[3] / "sage-wiki"
        )

    def test_default_binary_env_override(self, monkeypatch):
        monkeypatch.setenv("SAGE_WIKI_BIN", "/opt/custom/sage-wiki")
        assert default_binary() == "/opt/custom/sage-wiki"

    def test_require_api_key(self, monkeypatch):
        monkeypatch.delenv("OPENAI_API_KEY", raising=False)
        with pytest.raises(SystemExit):
            require_api_key()
        monkeypatch.setenv("OPENAI_API_KEY", "test")
        require_api_key()  # no raise
