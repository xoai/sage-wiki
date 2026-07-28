"""T5 — LOCOMO runner: rendering, end-to-end with stubs, malformed judge, resume."""

import json

import pytest

from benchmarks.locomo.run import (
    RunConfig,
    render_session,
    run_benchmark,
)

DATASET = [{
    "conversation": {
        "speaker_a": "Alice",
        "speaker_b": "Bob",
        "session_1_date_time": "1:56 pm on 8 May, 2023",
        "session_1": [
            {"speaker": "Alice", "text": "I adopted a dog named Biscuit."},
            {"speaker": "Bob", "text": "Congrats!", "blip_caption": "a golden retriever puppy"},
            {"speaker": "Alice", "text": ""},
        ],
        "session_2_date_time": "3:10 pm on 2 June, 2023",
        "session_2": [
            {"speaker": "Bob", "text": "I moved to Lisbon."},
        ],
    },
    "qa": [
        {"question": "What is the dog's name?", "answer": "Biscuit", "category": 4},
        {"question": "Where does Bob live?", "answer": "Lisbon", "category": 1},
        {"question": "Adversarial?", "answer": "n/a", "category": 5},
    ],
}]


class StubBackend:
    def __init__(self, degrade_qids=()):
        self.compiled = []
        self.sessions = {}
        self.degrade = set(degrade_qids)
        self.search_limits = []

    def init_project(self, key):
        self.sessions[key] = {}

    def write_session(self, key, session_id, markdown):
        self.sessions[key][session_id] = markdown

    def compile(self, key, timeout_s=0):
        from benchmarks.common.sagewiki import CompileStats
        self.compiled.append(key)
        return CompileStats(skipped=False, seconds=1.0, vector_count=5,
                            source_count=len(self.sessions[key]))

    def search(self, key, query, limit=10, timeout_s=0):
        from benchmarks.common.sagewiki import DegradedSearchError, SearchResponse
        self.search_limits.append(limit)
        if query in self.degrade:
            raise DegradedSearchError("degraded")
        return SearchResponse(
            results=[{"memory": "Alice adopted a dog named Biscuit.", "score": 0.9, "id": "m1"}],
            latency_ms=12.0)

    def binary_version(self):
        return "sage-wiki stub"


class StubLLM:
    """Answerer returns a fixed answer; judge scripts label per question text."""

    def __init__(self, judge_labels=None, judge_returns_none_for=()):
        self.judge_labels = judge_labels or {}
        self.none_for = set(judge_returns_none_for)
        self.json_calls = 0

    def generate(self, system, user):
        return "ANSWER: Biscuit"

    def generate_json(self, system, user):
        self.json_calls += 1
        for needle in self.none_for:
            if needle in user:
                return None
        for needle, label in self.judge_labels.items():
            if needle in user:
                return {"label": label, "reasoning": "scripted"}
        return {"label": "CORRECT", "reasoning": "scripted"}

    def usage(self):
        return {"prompt_tokens": 1, "completion_tokens": 1, "calls": 1}


class TestRenderSession:
    def test_date_in_heading_and_body_speakers_and_images(self):
        conv = DATASET[0]["conversation"]
        md = render_session("session_1", conv["session_1_date_time"],
                            conv["session_1"], "Alice", "Bob")
        assert md.splitlines()[0] == "# Conversation session on 1:56 pm on 8 May, 2023"
        assert "This session took place on 1:56 pm on 8 May, 2023." in md
        assert "**Alice:** I adopted a dog named Biscuit." in md
        assert "**Bob:** Congrats! [Sharing image that shows: a golden retriever puppy]" in md
        # empty-text turn dropped
        assert md.count("**Alice:**") == 1


def run_with(tmp_path, dataset=DATASET, judge=None, backend=None, **cfg_kw):
    cfg = RunConfig(out_dir=tmp_path / "out", results_dir=tmp_path / "results",
                    project_name="test", conversations=[0], **cfg_kw)
    backend = backend or StubBackend()
    judge = judge or StubLLM()
    return run_benchmark(cfg, backend, StubLLM(), judge, dataset), backend


class TestEndToEnd:
    def test_aggregate_and_records(self, tmp_path):
        agg, backend = run_with(tmp_path)
        # category 5 excluded → 2 questions
        assert agg["metrics"]["overall"]["total"] == 2
        assert agg["metrics"]["overall"]["accuracy"] == 100.0
        assert backend.compiled == ["conv0"]
        # session files rendered with date in name order
        assert set(backend.sessions["conv0"]) == {"session_1", "session_2"}
        # results JSON written
        files = list((tmp_path / "results").glob("locomo_*.json"))
        assert len(files) == 1
        data = json.loads(files[0].read_text())
        assert data["metadata"]["benchmark"] == "locomo"
        assert len(data["per_question"]) == 2

    def test_malformed_judge_marks_wrong_with_flag(self, tmp_path):
        judge = StubLLM(judge_returns_none_for={"What is the dog's name?"})
        agg, _ = run_with(tmp_path, judge=judge)
        rows = agg["per_question"]
        bad = [r for r in rows if r["question"] == "What is the dog's name?"][0]
        assert bad["judgment"] == "WRONG" and bad["judge_parse_error"] is True
        assert agg["metadata"]["judge_parse_errors"] == 1

    def test_degraded_search_becomes_infra_error(self, tmp_path):
        backend = StubBackend(degrade_qids={"Where does Bob live?"})
        agg, _ = run_with(tmp_path, backend=backend)
        assert agg["metrics"]["overall"]["total"] == 1  # excluded from denominator
        assert agg["metrics"]["overall"]["infra_errors"] == 1

    def test_resume_skips_done_questions(self, tmp_path):
        run_with(tmp_path)
        judge = StubLLM()
        agg, _ = run_with(tmp_path, judge=judge)
        assert judge.json_calls == 0  # everything resumed
        assert agg["metrics"]["overall"]["total"] == 2

    def test_max_questions_caps(self, tmp_path):
        agg, _ = run_with(tmp_path, max_questions=1)
        assert agg["metrics"]["overall"]["total"] == 1


class FailingCompileBackend(StubBackend):
    def compile(self, key, timeout_s=0):
        from benchmarks.common.sagewiki import CompileError
        raise CompileError("compile blew up")


class TestCompileFailureAccounting:
    def test_failed_project_recorded_in_run_metadata(self, tmp_path):
        agg, _ = run_with(tmp_path, backend=FailingCompileBackend())
        assert agg["metadata"]["failed_projects"] == ["conv0"]
        meta = json.loads((tmp_path / "out" / "_run_metadata.json").read_text())
        assert meta["failed_projects"] == ["conv0"]
        assert agg["metrics"]["overall"]["infra_errors"] == 2


class TestCutoffMode:
    """Parity mode (mem0-style): search once at max cutoff, answer+judge per cutoff."""

    def test_cutoffs_search_once_and_judge_per_cutoff(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "out", results_dir=tmp_path / "results",
                        project_name="parity", conversations=[0],
                        cutoffs=[2, 5])
        backend = StubBackend()
        judge = StubLLM()
        agg = run_benchmark(cfg, backend, StubLLM(), judge, DATASET)
        # one search per question at the max cutoff
        assert backend.search_limits == [5, 5]
        # per-cutoff results on each record and per-cutoff metrics in aggregate
        rows = agg["per_question"]
        assert all(set(r["cutoff_results"]) == {"top_2", "top_5"} for r in rows)
        assert set(agg["metrics_by_cutoff"]) == {"top_2", "top_5"}
        m2 = agg["metrics_by_cutoff"]["top_2"]["overall"]
        assert m2["total"] == 2 and m2["accuracy"] == 100.0
        # judge called once per cutoff per question
        assert judge.json_calls == 4

    def test_cutoff_mode_writes_results_and_flags_parse_errors(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "out", results_dir=tmp_path / "results",
                        project_name="parity", conversations=[0], cutoffs=[2, 5])
        judge = StubLLM(judge_returns_none_for={"What is the dog's name?"})
        agg = run_benchmark(cfg, StubBackend(), StubLLM(), judge, DATASET)
        bad = [r for r in agg["per_question"]
               if r["question"] == "What is the dog's name?"][0]
        assert bad["cutoff_results"]["top_2"]["judgment"] == "WRONG"
        assert bad["judge_parse_error"] is True

    def test_single_cutoff_default_unchanged(self, tmp_path):
        agg, backend = run_with(tmp_path)
        assert "metrics" in agg and "cutoff_results" not in agg["per_question"][0]
        assert backend.search_limits == [10, 10]


class TestQuotaAbort:
    """A sustained provider wall must stop the run, not convert the remaining
    queue into infra_error records (the 2026-07-28 parity-run failure: 1,011
    questions burned over 50 minutes after quota ran out)."""

    class WallBackend(StubBackend):
        def __init__(self, fail_after):
            super().__init__()
            self.fail_after = fail_after
            self.calls = 0

        def search(self, key, query, limit=10, timeout_s=0):
            from benchmarks.common.ratelimit import QuotaExhausted
            self.calls += 1
            if self.calls > self.fail_after:
                raise QuotaExhausted("provider wall")
            return super().search(key, query, limit=limit)

    def test_quota_exhausted_aborts_and_preserves_completed_work(self, tmp_path):
        from benchmarks.common.ratelimit import QuotaExhausted
        cfg = RunConfig(out_dir=tmp_path / "out", results_dir=tmp_path / "results",
                        project_name="t", conversations=[0], workers=1)
        with pytest.raises(QuotaExhausted):
            run_benchmark(cfg, self.WallBackend(fail_after=1), StubLLM(), StubLLM(),
                          DATASET)
        # the question completed before the wall is checkpointed and resumable
        done = list((tmp_path / "out").glob("conv0_q*.json"))
        assert len(done) == 1
        # and no infra_error record was manufactured for the aborted question
        assert all(json.loads(p.read_text())["status"] == "ok" for p in done)

    def test_run_metadata_records_the_abort(self, tmp_path):
        from benchmarks.common.ratelimit import QuotaExhausted
        cfg = RunConfig(out_dir=tmp_path / "out", results_dir=tmp_path / "results",
                        project_name="t", conversations=[0], workers=1)
        with pytest.raises(QuotaExhausted):
            run_benchmark(cfg, self.WallBackend(fail_after=0), StubLLM(), StubLLM(),
                          DATASET)
        meta = json.loads((tmp_path / "out" / "_run_metadata.json").read_text())
        assert meta["status"] == "aborted_quota"
        assert "resume" in meta["error"].lower()


class TestStratifiedSample:
    """--sample N draws a representative subset: category proportions preserved,
    spread across conversations, deterministic per seed."""

    def build_dataset(self, n_convs=10, per_cat=None):
        per_cat = per_cat or {1: 28, 2: 32, 3: 10, 4: 84}
        ds = []
        for c in range(n_convs):
            qa = []
            for cat, count in per_cat.items():
                for i in range(count):
                    qa.append({"question": f"c{c} cat{cat} q{i}", "answer": "a", "category": cat})
            ds.append({"conversation": {"speaker_a": "A", "speaker_b": "B"}, "qa": qa})
        return ds

    def test_returns_exactly_n(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset()
        got = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        assert len(got) == 150

    def test_category_proportions_track_population(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset()
        got = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        from collections import Counter
        counts = Counter(qa["category"] for _, _, qa in got)
        # population shares: cat4 54.5%, cat2 20.8%, cat1 18.2%, cat3 6.5%
        assert 78 <= counts[4] <= 86
        assert 28 <= counts[2] <= 34
        assert 24 <= counts[1] <= 31
        assert 7 <= counts[3] <= 12

    def test_spreads_across_conversations(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset()
        got = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        convs = {conv for conv, _, _ in got}
        assert convs == set(range(10)), f"every conversation should appear, got {sorted(convs)}"

    def test_deterministic_for_a_seed(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset()
        a = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        b = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        assert [(c, i) for c, i, _ in a] == [(c, i) for c, i, _ in b]

    def test_different_seed_gives_different_sample(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset()
        a = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=42)
        b = stratified_question_sample(ds, list(range(10)), [1, 2, 3, 4], 150, seed=7)
        assert [(c, i) for c, i, _ in a] != [(c, i) for c, i, _ in b]

    def test_n_larger_than_population_returns_all(self):
        from benchmarks.locomo.run import stratified_question_sample
        ds = self.build_dataset(n_convs=1, per_cat={1: 3, 4: 2})
        got = stratified_question_sample(ds, [0], [1, 2, 3, 4], 999, seed=42)
        assert len(got) == 5

    def test_runner_honors_sample(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="s", conversations=[0], sample=2)
        agg, _ = None, None
        backend = StubBackend()
        agg = run_benchmark(cfg, backend, StubLLM(), StubLLM(), DATASET)
        assert agg["metrics"]["overall"]["total"] == 2
        assert agg["metadata"]["scope"]["sample"] == 2
