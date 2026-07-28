"""T7 — BEAM runner: repr parsing, chat batches, nugget judge, tau supplement."""

import json

from benchmarks.beam.run import (
    RunConfig,
    clamp_nugget_score,
    extract_probing_questions,
    extract_rubric_nuggets,
    normalize_record,
    parse_beam_chat,
    render_batch,
    run_benchmark,
)

RAW_RECORD = {
    "conversation_id": "1",
    "chat": repr([[{"role": "user", "content": "I like tea.", "time_anchor": "March 15, 2024"},
                   {"role": "assistant", "content": "Noted!"}],
                  [{"role": "user", "content": "Now I prefer coffee."}]]),
    "probing_questions": repr({
        "preference_following": [
            {"question": "What drink does the user prefer now?",
             "rubric": {"nuggets": [{"description": "User now prefers coffee"},
                                    "Preference changed from tea"]}},
        ],
        "event_ordering": [
            {"question": "Order the drink preferences.",
             "rubric": {"nuggets": ["Liked tea first", "Then preferred coffee"]}},
        ],
    }),
}


class TestParsing:
    def test_normalize_record_parses_repr_strings(self):
        rec = normalize_record(RAW_RECORD)
        assert isinstance(rec["chat"], list) and isinstance(rec["probing_questions"], dict)

    def test_parse_beam_chat_2d_list(self):
        rec = normalize_record(RAW_RECORD)
        batches = parse_beam_chat(rec["chat"])
        assert len(batches) == 2 and batches[0][0]["content"] == "I like tea."

    def test_parse_beam_chat_batch_dict_format(self):
        chat = [{"turns": [[{"role": "user", "content": "a"}], {"role": "assistant", "content": "b"}]}]
        batches = parse_beam_chat(chat)
        assert batches == [[{"role": "user", "content": "a"}, {"role": "assistant", "content": "b"}]]

    def test_extract_probing_questions_tags_types(self):
        rec = normalize_record(RAW_RECORD)
        qs = extract_probing_questions(rec)
        assert {q["question_type"] for q in qs} == {"preference_following", "event_ordering"}

    def test_extract_rubric_nuggets_mixed(self):
        rec = normalize_record(RAW_RECORD)
        q = [x for x in extract_probing_questions(rec)
             if x["question_type"] == "preference_following"][0]
        assert extract_rubric_nuggets(q) == ["User now prefers coffee",
                                             "Preference changed from tea"]


class TestClamp:
    def test_bands(self):
        assert clamp_nugget_score(0.9) == 1.0
        assert clamp_nugget_score(0.5) == 0.5
        assert clamp_nugget_score(0.1) == 0.0


class TestRenderBatch:
    def test_heading_anchor_and_roles(self):
        rec = normalize_record(RAW_RECORD)
        batches = parse_beam_chat(rec["chat"])
        md = render_batch(1, batches[0])
        assert md.splitlines()[0] == "# Conversation batch 1 (around March 15, 2024)"
        assert "**User:** I like tea." in md and "**Assistant:** Noted!" in md
        md2 = render_batch(2, batches[1])
        assert md2.splitlines()[0] == "# Conversation batch 2"


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
        return SearchResponse([{"memory": "User switched from tea to coffee.",
                                "score": 1.0, "id": "m"}], 7.0)

    def binary_version(self):
        return "stub"


class StubJudge:
    """Nugget judge scoring 1.0; event extraction/alignment scripted."""

    def __init__(self, nugget_json={"score": 1.0}, fail_nuggets=False):
        self.nugget_json = nugget_json
        self.fail_nuggets = fail_nuggets
        self.model = "stub"

    def generate(self, system, user):
        return "The user liked tea, then coffee."

    def generate_json(self, system, user):
        if "RUBRIC CRITERION" in user or "rubric criterion" in user.lower():
            return None if self.fail_nuggets else dict(self.nugget_json)
        if "Extract" in system or "extract" in user.lower():
            return {"events": ["liked tea", "prefers coffee"]}
        if "index" in user.lower() or "Align" in system:
            return {"index": 0 if "tea" in user else 1}
        return {}

    def usage(self):
        return {"calls": 1, "prompt_tokens": 1, "completion_tokens": 1}


class StubAnswerer(StubJudge):
    pass


class TestEndToEnd:
    def test_aggregate_with_tau_supplement(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0])
        agg = run_benchmark(cfg, StubBackend(), StubAnswerer(), StubJudge(),
                            [normalize_record(RAW_RECORD)])
        assert agg["metrics"]["overall"]["total"] == 2
        eo = agg["metrics"]["by_group"]["event_ordering"]
        assert eo["avg_score"] == 100.0            # nugget mean stays primary
        assert "score_with_tau" in eo              # tau recorded as supplement
        rows = agg["per_question"]
        eo_row = [r for r in rows if r["group"] == "event_ordering"][0]
        assert "tau_b" in eo_row

    def test_nugget_parse_failure_scores_zero_with_flag(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0])
        agg = run_benchmark(cfg, StubBackend(), StubAnswerer(),
                            StubJudge(fail_nuggets=True),
                            [normalize_record(RAW_RECORD)])
        assert agg["metrics"]["overall"]["avg_score"] == 0.0
        assert agg["metadata"]["judge_parse_errors"] == 2
        assert all(r["judge_parse_error"] for r in agg["per_question"])


class TestCutoffMode:
    def test_nuggets_judged_at_each_cutoff(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0], cutoffs=[1, 5])
        backend = StubBackend()
        agg = run_benchmark(cfg, backend, StubAnswerer(), StubJudge(),
                            [normalize_record(RAW_RECORD)])
        assert backend.search_limits == [5, 5]      # one search at the max cutoff
        assert set(agg["metrics_by_cutoff"]) == {"top_1", "top_5"}
        for r in agg["per_question"]:
            assert set(r["cutoff_results"]) == {"top_1", "top_5"}
            # nugget mean stays the primary score at every cutoff
            assert r["cutoff_results"]["top_5"]["score"] == 1.0
            assert r["cutoff_results"]["top_5"]["nugget_scores"]

    def test_event_ordering_tau_recorded_at_max_cutoff(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0], cutoffs=[1, 5])
        agg = run_benchmark(cfg, StubBackend(), StubAnswerer(), StubJudge(),
                            [normalize_record(RAW_RECORD)])
        eo = [r for r in agg["per_question"] if r["group"] == "event_ordering"][0]
        assert "tau_b" in eo

    def test_nugget_parse_failure_flags_at_cutoffs(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0], cutoffs=[1, 5])
        agg = run_benchmark(cfg, StubBackend(), StubAnswerer(),
                            StubJudge(fail_nuggets=True), [normalize_record(RAW_RECORD)])
        assert all(r["judge_parse_error"] for r in agg["per_question"])
        assert all(c["score"] == 0.0
                   for r in agg["per_question"] for c in r["cutoff_results"].values())

    def test_single_cutoff_default_unchanged(self, tmp_path):
        cfg = RunConfig(out_dir=tmp_path / "o", results_dir=tmp_path / "r",
                        project_name="t", conversations=[0])
        agg = run_benchmark(cfg, StubBackend(), StubAnswerer(), StubJudge(),
                            [normalize_record(RAW_RECORD)])
        assert "metrics_by_cutoff" not in agg
        assert "cutoff_results" not in agg["per_question"][0]
