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


def fast_gate():
    """Real gate semantics, zero wall-clock — unit tests must never sleep."""
    from benchmarks.common.ratelimit import RateLimitGate
    return RateLimitGate(base_delay=0.0, max_delay=0.0, give_up_after=1e9,
                         sleep=lambda s: None, jitter=lambda: 0.0)


@pytest.fixture()
def backend(tmp_path):
    return SageWikiBackend(binary=str(FAKE), root=tmp_path / "projects",
                           gate=fast_gate())


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
        assert "mode: standard" in cfg  # auto mode batch-submits at >=10 sources
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


class TestGarbageStdout:
    def test_undecodable_stdout_becomes_degraded_error(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "garbage-stdout")
        backend.init_project("conv0")
        with pytest.raises(DegradedSearchError, match="undecodable"):
            backend.search("conv0", "q", limit=10)


class TestNoopCompileGuard:
    """C-1/M-1 regression: an interrupted compile followed by a no-op resume
    (exit 0, '0 summarized', one stray vector) must FAIL the compile gate —
    it silently benchmarks raw transcripts otherwise."""

    def test_noop_compile_with_stray_vector_fails(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "noop-compile")
        backend.init_project("conv0")
        for i in range(3):
            backend.write_session("conv0", f"s{i}", "hi")
        with pytest.raises(CompileError, match="compiled source count"):
            backend.compile("conv0")
        assert not (backend.project_dir("conv0") / ".compiled").exists()

    def test_healthy_compile_passes_hardened_guard(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "ok")
        backend.init_project("conv0")
        backend.write_session("conv0", "s1", "hi")
        assert backend.compile("conv0").vector_count == 42

    def test_poisoned_marker_does_not_skip_the_gate(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "noop-compile")
        backend.init_project("conv0")
        for i in range(3):
            backend.write_session("conv0", f"s{i}", "hi")
        (backend.project_dir("conv0") / ".compiled").write_text("3")
        with pytest.raises(CompileError, match="marker present but project is"):
            backend.compile("conv0")


class TestSearchRateLimitDegrade:
    """A rate-limited embedder degrades search to BM25-only with exit 0 — the
    2026-07-28 parity-run failure. Retries must back off through the shared
    gate instead of firing instantly and giving up."""

    def test_rate_limit_degrade_reports_to_gate_and_retries(self, backend, tmp_path, monkeypatch):
        from benchmarks.common.ratelimit import RateLimitGate
        events = {"limit": 0, "wait": 0}

        class SpyGate(RateLimitGate):
            def wait(self):
                events["wait"] += 1

            def report_limit(self, retry_after=None):
                events["limit"] += 1
                return 0.0

        backend.gate = SpyGate()
        set_fixture(tmp_path, monkeypatch, "embed-ratelimit")
        backend.init_project("conv0")
        with pytest.raises(DegradedSearchError, match="rate"):
            backend.search("conv0", "q", limit=10)
        assert events["limit"] >= 1      # provider pressure was reported
        assert events["wait"] >= 1       # and waited on before retrying

    def test_rate_limit_degrade_gets_more_attempts_than_permanent_degrade(
            self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "embed-ratelimit")
        backend.init_project("conv0")
        with pytest.raises(DegradedSearchError):
            backend.search("conv0", "q", limit=10)
        rate_calls = int((tmp_path / "fake-state" / "search-calls").read_text())

        set_fixture(tmp_path, monkeypatch, "bm25-only")   # permanent (config) degrade
        backend.init_project("conv1")
        with pytest.raises(DegradedSearchError):
            backend.search("conv1", "q", limit=10)
        perm_calls = int((tmp_path / "fake-state" / "search-calls").read_text()) - rate_calls
        assert rate_calls > perm_calls

    def test_transient_rate_limit_recovers(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "embed-ratelimit-once")
        backend.init_project("conv0")
        assert len(backend.search("conv0", "q", limit=10).results) == 2


class TestFakeBinaryIsExecutableInGit:
    """The stub's exec bit must live in git, not in a local chmod: without it
    every subprocess test fails on a fresh clone while passing for whoever
    ran chmod locally (caught post-merge, 2026-07-28)."""

    def test_stub_is_executable(self):
        assert os.access(FAKE, os.X_OK), (
            f"{FAKE} is not executable — run: "
            f"git update-index --chmod=+x {FAKE.relative_to(Path.cwd()) if FAKE.is_relative_to(Path.cwd()) else FAKE}"
        )


class TestNewSearchPipelineContract:
    """The arch/search-upgrade pipeline changed the degrade warning text and
    added FinalScore as the authoritative rank. Both must be handled, or the
    rate-limit path goes dead and reported scores stop matching the ranking."""

    NEW_DEGRADE = ('level=WARN msg="query embedding failed for every variant — '
                   'vector legs skipped, results are lexical/graph only" failed=1 '
                   'error="llm: rate limited (HTTP 429): insufficient_quota"')
    OLD_DEGRADE = "warning: embed failed, using BM25-only: dial tcp: timeout"

    def test_new_degrade_warning_is_recognized(self):
        assert SageWikiBackend._degraded(self.NEW_DEGRADE, [{"VectorRank": 3}])

    def test_old_degrade_warning_still_recognized(self):
        assert SageWikiBackend._degraded(self.OLD_DEGRADE, [{"VectorRank": 3}])

    def test_new_rate_limit_degrade_is_classified_transient(self):
        assert SageWikiBackend._rate_limited(self.NEW_DEGRADE)

    def test_new_degrade_without_rate_limit_is_permanent(self):
        cfg_fail = ('level=WARN msg="query embedding failed for every variant — '
                    'vector legs skipped, results are lexical/graph only" '
                    'error="no embedding provider configured"')
        assert SageWikiBackend._degraded(cfg_fail, [{"VectorRank": 1}])
        assert not SageWikiBackend._rate_limited(cfg_fail)

    def test_healthy_search_is_not_flagged(self):
        assert not SageWikiBackend._degraded("", [{"VectorRank": 2}])

    def test_score_prefers_final_score_when_present(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "final-score")
        backend.init_project("conv0")
        resp = backend.search("conv0", "q", limit=10)
        # FinalScore is what the new pipeline ranks by (search/pipeline.go:222)
        assert [r["score"] for r in resp.results] == [1.0, 0.4919]

    def test_score_falls_back_to_rrf_for_older_binaries(self, backend, tmp_path, monkeypatch):
        set_fixture(tmp_path, monkeypatch, "ok")   # fixture has no FinalScore
        backend.init_project("conv0")
        resp = backend.search("conv0", "q", limit=10)
        assert resp.results[0]["score"] == pytest.approx(0.03)
