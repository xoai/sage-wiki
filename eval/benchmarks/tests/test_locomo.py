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
