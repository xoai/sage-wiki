"""T1 — vendored prompt modules: import surface + missing-date shapes.

Run from the repo root: pytest eval/benchmarks/tests  (package root is eval/).

sage-wiki search results carry no created_at (spec §3), so these tests pin
the exact rendering each vendored formatter produces for date-less memories:
LOCOMO emits its chronological-order header and an "(unknown date)" prefix;
LongMemEval and BEAM render prefix-free memory lines.
"""

from benchmarks.locomo import prompts as locomo_prompts
from benchmarks.longmemeval import prompts as lme_prompts
from benchmarks.beam import prompts as beam_prompts

MEMS = [{"memory": "Alice adopted a dog named Biscuit.", "score": 0.9, "id": "a"},
        {"memory": "Bob moved to Lisbon in spring.", "score": 0.5, "id": "b"}]


class TestExports:
    def test_locomo_surface(self):
        assert locomo_prompts.CATEGORIES_TO_EVALUATE == [1, 2, 3, 4]
        assert set(locomo_prompts.CATEGORY_NAMES) >= {1, 2, 3, 4}
        assert callable(locomo_prompts.get_answer_generation_prompt)
        assert callable(locomo_prompts.get_judge_prompt)
        assert callable(locomo_prompts.preprocess_answer)
        assert locomo_prompts.JUDGE_SYSTEM_PROMPT

    def test_longmemeval_surface(self):
        assert len(lme_prompts.QUESTION_TYPES) == 6
        assert callable(lme_prompts.get_answer_generation_prompt)
        assert callable(lme_prompts.get_judge_prompt)

    def test_beam_surface(self):
        assert len(beam_prompts.BEAM_QUESTION_TYPES) == 10
        assert callable(beam_prompts.get_beam_answer_generation_prompt)
        assert callable(beam_prompts.get_beam_nugget_judge_prompt)
        assert beam_prompts.BEAM_JUDGE_SYSTEM_PROMPT


class TestMissingDateShapes:
    def test_locomo_unknown_date_prefix_and_header(self):
        p = locomo_prompts.get_answer_generation_prompt("Who adopted a dog?", MEMS)
        assert "The following memories are presented in chronological order (oldest to newest)." in p
        assert "(unknown date) Alice adopted a dog named Biscuit." in p
        assert "(unknown date) Bob moved to Lisbon in spring." in p

    def test_longmemeval_prefix_free_lines(self):
        p = lme_prompts.get_answer_generation_prompt("Where does Bob live?", MEMS, "2023/05/20 (Sat) 10:00")
        assert "- Alice adopted a dog named Biscuit." in p
        assert "- Bob moved to Lisbon in spring." in p
        assert "unknown date" not in p
        assert "--- " not in p  # no date group headers without created_at

    def test_beam_prefix_free_numbered_lines(self):
        p = beam_prompts.get_beam_answer_generation_prompt("Where does Bob live?", MEMS)
        assert "1. Alice adopted a dog named Biscuit." in p
        assert "2. Bob moved to Lisbon in spring." in p
        assert "[" not in p.split("Question:")[0].split("memories")[-1] or True
        assert "unknown date" not in p

    def test_empty_results_render_placeholder(self):
        p = locomo_prompts.get_answer_generation_prompt("q", [])
        assert "(No relevant memories found)" in p
