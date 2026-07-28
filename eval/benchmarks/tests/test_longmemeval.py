"""T6 — LongMemEval runner: sampling, rendering, yes/no judge, end-to-end stubs."""

import json

from benchmarks.longmemeval.run import (
    RunConfig,
    parse_yes_no,
    render_session,
    run_benchmark,
    stratified_sample,
)

def make_question(qid, qtype, n_sessions=2):
    return {
        "question_id": qid,
        "question_type": qtype,
        "question": f"Q {qid}?",
        "answer": "42",
        "question_date": "2023/05/20 (Sat) 10:00",
        "haystack_session_ids": [f"s{i}" for i in range(n_sessions)],
        "haystack_dates": [f"2023/05/0{i + 1} (Mon) 10:0{i}" for i in range(n_sessions)],
        "haystack_sessions": [
            [{"role": "user", "content": f"hello {i}", "has_answer": bool(i)},
             {"role": "assistant", "content": f"hi {i}"}]
            for i in range(n_sessions)
        ],
    }


DATASET = [
    make_question("q_a", "multi-session"),
    make_question("q_b", "multi-session"),
    make_question("q_c", "temporal-reasoning"),
    make_question("q_abs_1_abs", "knowledge-update"),
]


class TestSampling:
    def test_stratified_deterministic_and_capped(self):
        s1 = stratified_sample(DATASET, per_type=1, seed=42)
        s2 = stratified_sample(DATASET, per_type=1, seed=42)
        assert [q["question_id"] for q in s1] == [q["question_id"] for q in s2]
        types = [q["question_type"] for q in s1]
        assert types.count("multi-session") == 1
        assert len(s1) == 3  # one per present type


class TestRendering:
    def test_session_markdown_has_date_and_roles(self):
        q = DATASET[0]
        md = render_session("s0", q["haystack_dates"][0], q["haystack_sessions"][0])
        assert md.splitlines()[0].startswith("# Chat session on 2023/05/01")
        assert "This chat session took place on 2023/05/01" in md
        assert "**User:** hello 0" in md
        assert "**Assistant:** hi 0" in md


class TestParseYesNo:
    def test_verdicts(self):
        assert parse_yes_no("Yes") == (True, False)
        assert parse_yes_no("reasoning...\nno") == (False, False)
        assert parse_yes_no("The answer is yes I think") == (True, False)
        assert parse_yes_no("") == (False, True)
        assert parse_yes_no("inscrutable") == (False, True)


class StubBackend:
    def __init__(self):
        self.projects = {}
        self.search_limits = []

    def init_project(self, key):
        self.projects.setdefault(key, {})

    def write_session(self, key, sid, md):
        self.projects[key][sid] = md

    def compile(self, key, timeout_s=0):
        from benchmarks.common.sagewiki import CompileStats
        return CompileStats(False, 1.0, 3, len(self.projects[key]))

    def search(self, key, query, limit=10, timeout_s=0):
        from benchmarks.common.sagewiki import SearchResponse
        self.search_limits.append(limit)
        return SearchResponse([{"memory": "the answer is 42", "score": 1.0, "id": "m"}], 8.0)

    def binary_version(self):
        return "stub"


class StubLLM:
    def __init__(self, judge_text="yes"):
        self.judge_text = judge_text
        self.model = "stub"

    def generate(self, system, user):
        if "model response" in user or "correct answer" in user:
            return self.judge_text
        return "ANSWER: 42"

    def usage(self):
        return {"calls": 1, "prompt_tokens": 1, "completion_tokens": 1}


class TestEndToEnd:
    def test_aggregate(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5)
        agg = run_benchmark(cfg, StubBackend(), StubLLM(), StubLLM(), DATASET)
        assert agg["metrics"]["overall"]["total"] == 4
        assert agg["metrics"]["overall"]["accuracy"] == 100.0
        assert "multi-session" in agg["metrics"]["by_group"]
        files = list((tmp_path / "r").glob("longmemeval_*.json"))
        assert len(files) == 1
        rows = json.loads(files[0].read_text())["per_question"]
        abst = [r for r in rows if r["question_id"].endswith("_abs")][0]
        assert abst["is_abstention"] is True

    def test_unparseable_judge_fails_with_flag(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5)
        agg = run_benchmark(cfg, StubBackend(), StubLLM(),
                            StubLLM(judge_text="inscrutable"), DATASET)
        assert agg["metrics"]["overall"]["accuracy"] == 0.0
        assert agg["metadata"]["judge_parse_errors"] == 4
        row = agg["per_question"][0]
        assert row["judgment"] == "FAIL" and row["judge_parse_error"] is True


class TestYesPrefixedGarbageJudge:
    def test_parse_failure_never_scores_pass(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5)
        judge = StubLLM(judge_text="Yesterday's diary entry confirms the outing.")
        agg = run_benchmark(cfg, StubBackend(), StubLLM(), judge, DATASET)
        assert agg["metrics"]["overall"]["accuracy"] == 0.0
        for r in agg["per_question"]:
            assert r["judgment"] == "FAIL" and r["score"] == 0.0
            assert r["judge_parse_error"] is True


class TestCutoffMode:
    def test_cutoffs_search_once_and_judge_per_cutoff(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5, cutoffs=[2, 5])
        backend = StubBackend()
        agg = run_benchmark(cfg, backend, StubLLM(), StubLLM(), DATASET)
        assert backend.search_limits == [5] * 4          # one search at max cutoff
        assert set(agg["metrics_by_cutoff"]) == {"top_2", "top_5"}
        rows = agg["per_question"]
        assert all(set(r["cutoff_results"]) == {"top_2", "top_5"} for r in rows)
        assert agg["metrics_by_cutoff"]["top_2"]["overall"]["total"] == 4

    def test_parse_failure_never_passes_at_any_cutoff(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5, cutoffs=[2, 5])
        agg = run_benchmark(cfg, StubBackend(), StubLLM(),
                            StubLLM(judge_text="Yesterday's entry confirms it."), DATASET)
        for r in agg["per_question"]:
            assert all(c["judgment"] == "FAIL" for c in r["cutoff_results"].values())
            assert r["judge_parse_error"] is True

    def test_single_cutoff_default_unchanged(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", per_type=5)
        agg = run_benchmark(cfg, StubBackend(), StubLLM(), StubLLM(), DATASET)
        assert "metrics_by_cutoff" not in agg
        assert "cutoff_results" not in agg["per_question"][0]
