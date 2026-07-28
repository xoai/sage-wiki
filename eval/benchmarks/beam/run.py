"""BEAM benchmark runner with sage-wiki as the memory backend.

Pipeline design and parsing logic follow mem0ai/memory-benchmarks
(Apache-2.0, see NOTICE): chat/probing_questions arrive from HuggingFace as
Python-repr strings; per-nugget rubric judging (0/0.5/1.0, mean is the
primary question score for every type); event_ordering additionally records
Kendall tau-b and score_with_tau as supplementary fields.

Usage (from the eval/ directory):
    python -m benchmarks.beam.run --project-name my-run --chat-sizes 100K --conversations 0-2
"""

from __future__ import annotations

import argparse
import ast
import concurrent.futures
import json
import logging
import subprocess
import threading
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

from benchmarks.common.checkpoints import CheckpointStore, heartbeat, write_run_metadata
from benchmarks.common.llm import LLMClient, LLMError
from benchmarks.common.ratelimit import GLOBAL_GATE, QuotaExhausted
from benchmarks.common.metrics import (
    aggregate_beam,
    compute_kendall_tau_b,
    latency_stats,
    nugget_question_score,
)
from benchmarks.common.sagewiki import (
    CompileError,
    DegradedSearchError,
    SageWikiBackend,
    require_api_key,
)

from .prompts import (
    BEAM_JUDGE_SYSTEM_PROMPT,
    BEAM_QUESTION_TYPES,
    get_beam_answer_generation_prompt,
    get_beam_event_alignment_prompt,
    get_beam_fact_extraction_prompt,
    get_beam_nugget_judge_prompt,
)

log = logging.getLogger("beam")

HF_DATASET = "Mohammadta/BEAM"
PKG_ROOT = Path(__file__).resolve().parents[1]
DATASET_DIR = PKG_ROOT / "runs" / "datasets" / "beam"


# ---------------------------------------------------------------------------
# Dataset + parsing (upstream ports)
# ---------------------------------------------------------------------------


def _literal(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    try:
        return ast.literal_eval(value)
    except (ValueError, SyntaxError):
        try:
            return json.loads(value)
        except json.JSONDecodeError:
            return value


def normalize_record(item: dict) -> dict:
    """HF rows store chat/probing_questions as Python-repr strings — parse them."""
    rec = dict(item)
    rec["chat"] = _literal(rec.get("chat", []))
    pq = _literal(rec.get("probing_questions", {}))
    rec["probing_questions"] = pq if isinstance(pq, dict) else {}
    return rec


def load_dataset(size: str) -> list[dict]:
    """Load a BEAM size bucket via HF datasets, cached as normalized JSON."""
    cache = DATASET_DIR / f"beam_{size}.json"
    if cache.is_file():
        return json.loads(cache.read_text(encoding="utf-8"))
    from datasets import load_dataset as hf_load  # type: ignore[import-not-found]

    ds = hf_load(HF_DATASET, split=size)
    records = [normalize_record(dict(item)) for item in ds]
    cache.parent.mkdir(parents=True, exist_ok=True)
    cache.write_text(json.dumps(records, ensure_ascii=False), encoding="utf-8")
    return records


def _unwrap_batch_dicts(batch_dicts: list[dict]) -> list[list[dict]]:
    batches: list[list[dict]] = []
    for batch in batch_dicts:
        turns = batch.get("turns", [])
        flat: list[dict] = []
        for item in turns:
            if isinstance(item, list):
                flat.extend(item)
            elif isinstance(item, dict):
                flat.append(item)
        batches.append(flat)
    return batches


def parse_beam_chat(chat_data: Any) -> list[list[dict]]:
    """Upstream port — handles the three HF storage formats."""
    if not chat_data:
        return []
    if (isinstance(chat_data, list) and chat_data
            and isinstance(chat_data[0], dict) and "turns" in chat_data[0]):
        return _unwrap_batch_dicts(chat_data)
    if (isinstance(chat_data, list) and chat_data
            and isinstance(chat_data[0], dict) and "turns" not in chat_data[0]):
        first = chat_data[0]
        sample = next(iter(first.values()), None)
        if (isinstance(sample, list) and sample and isinstance(sample[0], dict)
                and "turns" in sample[0]):
            batches: list[list[dict]] = []
            for session in chat_data:
                if not isinstance(session, dict):
                    continue
                keys = sorted(session.keys(),
                              key=lambda k: int(k.split("-")[-1])
                              if k.split("-")[-1].isdigit() else 0)
                for k in keys:
                    if session[k] is not None:
                        batches.extend(_unwrap_batch_dicts(session[k]))
            return batches
        if "role" in first or "content" in first:
            return [chat_data]
        return []
    if isinstance(chat_data, list) and chat_data and isinstance(chat_data[0], list):
        return chat_data
    return []


def extract_probing_questions(conversation: dict) -> list[dict]:
    pq = conversation.get("probing_questions", {})
    if not isinstance(pq, dict):
        return []
    questions: list[dict] = []
    for q_type in BEAM_QUESTION_TYPES:
        type_questions = pq.get(q_type, [])
        if isinstance(type_questions, list):
            for q in type_questions:
                if isinstance(q, dict):
                    q = dict(q)
                    q["question_type"] = q_type
                    questions.append(q)
                else:
                    questions.append({"question_type": q_type,
                                      "question_text": q, "rubric": []})
        elif isinstance(type_questions, dict):
            tq = dict(type_questions)
            tq["question_type"] = q_type
            questions.append(tq)
    return questions


def extract_rubric_nuggets(question_data: dict) -> list[str]:
    rubric_raw = question_data.get("rubric", {})
    if isinstance(rubric_raw, dict):
        return [
            n.get("description", str(n)) if isinstance(n, dict) else str(n)
            for n in rubric_raw.get("nuggets", [])
        ]
    if isinstance(rubric_raw, list):
        return [str(n) for n in rubric_raw]
    if rubric_raw:
        return [str(rubric_raw)]
    return []


def clamp_nugget_score(raw_score: float) -> float:
    if raw_score >= 0.75:
        return 1.0
    if raw_score >= 0.25:
        return 0.5
    return 0.0


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------


def render_batch(batch_num: int, turns: list[dict]) -> str:
    """One markdown source per chat batch; the time anchor (when a turn carries
    one) lands in the heading and body — dates have no other route into
    sage-wiki search results (spec §3)."""
    anchor = next((t.get("time_anchor") for t in turns if t.get("time_anchor")), None)
    title = f"# Conversation batch {batch_num}"
    if anchor:
        title += f" (around {anchor})"
    lines = [title, ""]
    if anchor:
        lines += [f"This part of the conversation took place around {anchor}.", ""]
    for turn in turns:
        content = (turn.get("content") or "").strip()
        if not content:
            continue
        role = turn.get("role", "user")
        role = "User" if str(role).lower() in ("human", "user") else "Assistant"
        lines.append(f"**{role}:** {content}")
        lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Judging
# ---------------------------------------------------------------------------


def judge_nuggets(judge_llm, question: str, nuggets: list[str],
                  answer: str) -> tuple[list[dict], bool]:
    """Per-nugget 0/0.5/1.0; a parse failure (generate_json → None/invalid)
    scores that nugget 0.0 and flags the question (spec §5)."""
    scores: list[dict] = []
    any_parse_error = False
    for nugget in nuggets:
        prompt = get_beam_nugget_judge_prompt(question, nugget, answer)
        raw = judge_llm.generate_json(BEAM_JUDGE_SYSTEM_PROMPT, prompt)
        if isinstance(raw, dict) and "score" in raw:
            try:
                score = clamp_nugget_score(float(raw.get("score", 0.0)))
                scores.append({"nugget": nugget, "score": score,
                               "reason": str(raw.get("reason", ""))})
                continue
            except (TypeError, ValueError):
                pass
        any_parse_error = True
        scores.append({"nugget": nugget, "score": 0.0, "reason": "judge_parse_error"})
    return scores, any_parse_error


def event_ordering_supplement(judge_llm, question: str, nuggets: list[str],
                              answer: str) -> dict:
    """Upstream port: extract ordered events → align to rubric → tau-b."""
    raw = judge_llm.generate_json("Extract events as a JSON array of strings.",
                                  get_beam_fact_extraction_prompt(answer))
    events: list[str] = []
    if isinstance(raw, dict):
        for key in ("events", "facts", "result"):
            if isinstance(raw.get(key), list):
                events = raw[key]
                break
    if not events or not nuggets:
        return {"tau_b": 0.0, "predicted_order": [], "reference_order": []}

    predicted: list[int] = []
    for event in events:
        align = judge_llm.generate_json(
            "Align the event to a reference event index. Return JSON.",
            get_beam_event_alignment_prompt(str(event), nuggets))
        idx = -1
        if isinstance(align, dict):
            try:
                idx = int(align.get("index", -1))
            except (TypeError, ValueError):
                idx = -1
        if 0 <= idx < len(nuggets):
            predicted.append(idx)

    reference = list(range(len(nuggets)))
    return {"tau_b": compute_kendall_tau_b(predicted, reference),
            "predicted_order": predicted, "reference_order": reference}


# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------


@dataclass
class RunConfig:
    out_dir: Path
    results_dir: Path
    project_name: str
    chat_size: str = "100K"
    conversations: list[int] = field(default_factory=lambda: [0, 1, 2])
    top_k: int = 10
    cutoffs: list[int] | None = None  # parity mode: search at max, judge per cutoff
    max_questions: int | None = None
    workers: int = 4
    heartbeat_every: int = 25


def _process_question(cfg: RunConfig, backend, answerer, judge_llm,
                      key: str, conv_idx: int, qi: int, q: dict) -> dict:
    qid = f"conv{conv_idx}_q{qi}"
    question_text = q.get("question") or q.get("question_text") or ""
    nuggets = extract_rubric_nuggets(q)
    record = {
        "question_id": qid,
        "conversation_idx": conv_idx,
        "group": q.get("question_type", "unknown"),
        "question": question_text,
        "rubric_nuggets": nuggets,
        "status": "ok",
        "judge_parse_error": False,
    }
    try:
        limit = max(cfg.cutoffs) if cfg.cutoffs else cfg.top_k
        resp = backend.search(key, question_text, limit=limit)
        record["search_latency_ms"] = resp.latency_ms
        record["retrieval"] = resp.results

        if cfg.cutoffs:
            # The rubric judge is per-nugget, so every nugget is re-judged at
            # each depth — cost scales with cutoffs x nuggets, not cutoffs.
            cutoff_results: dict[str, dict] = {}
            any_parse_err = False
            deepest = max(cfg.cutoffs)
            for c in sorted(cfg.cutoffs):
                sliced = resp.results[:c]
                answer_c = answerer.generate("", get_beam_answer_generation_prompt(
                    question_text, sliced, top_k=c))
                scores_c, parse_c = judge_nuggets(judge_llm, question_text, nuggets, answer_c)
                any_parse_err = any_parse_err or parse_c
                score_c = nugget_question_score([n["score"] for n in scores_c])
                cutoff_results[f"top_{c}"] = {
                    "judgment": "PASS" if score_c >= 0.5 else "FAIL",
                    "score": round(score_c, 4),
                    "generated_answer": answer_c,
                    "memories_evaluated": len(sliced),
                    "nugget_scores": scores_c,
                }
                if c == deepest:
                    record["generated_answer"] = answer_c
            record.update(cutoff_results=cutoff_results,
                          judge_parse_error=any_parse_err,
                          score=cutoff_results[f"top_{deepest}"]["score"],
                          judgment=cutoff_results[f"top_{deepest}"]["judgment"],
                          nugget_scores=cutoff_results[f"top_{deepest}"]["nugget_scores"])
            if q.get("question_type") == "event_ordering":
                try:
                    supplement = event_ordering_supplement(
                        judge_llm, question_text, nuggets, record["generated_answer"])
                    record["tau_b"] = supplement["tau_b"]
                    record["event_ordering"] = supplement
                except LLMError as exc:
                    record["event_ordering"] = {"error": str(exc)}
            return record

        answer = answerer.generate("", get_beam_answer_generation_prompt(
            question_text, resp.results, top_k=cfg.top_k))
        record["generated_answer"] = answer
        nugget_scores, parse_err = judge_nuggets(judge_llm, question_text, nuggets, answer)
    except QuotaExhausted:
        raise  # provider wall: stop the run, do not fabricate a failed question
    except (DegradedSearchError, LLMError, subprocess.TimeoutExpired) as exc:
        record.update(status="infra_error", error=str(exc), score=0.0)
        record.setdefault("search_latency_ms", None)
        return record
    score = nugget_question_score([n["score"] for n in nugget_scores])
    record.update(
        nugget_scores=nugget_scores,
        score=round(score, 4),
        judgment="PASS" if score >= 0.5 else "FAIL",
        judge_parse_error=parse_err,
    )
    if q.get("question_type") == "event_ordering":
        try:
            supplement = event_ordering_supplement(judge_llm, question_text, nuggets, answer)
            record["tau_b"] = supplement["tau_b"]
            record["event_ordering"] = supplement
        except LLMError as exc:
            record["event_ordering"] = {"error": str(exc)}  # supplement only — question keeps its nugget score
    return record


def run_benchmark(cfg: RunConfig, backend, answerer, judge_llm,
                  dataset: list[dict]) -> dict:
    store = CheckpointStore(cfg.out_dir)
    write_run_metadata(cfg.out_dir, benchmark="beam", project_name=cfg.project_name,
                       binary_version=backend.binary_version(),
                       models={"answerer": getattr(answerer, "model", "?"),
                               "judge": getattr(judge_llm, "model", "?")},
                       scope={"chat_size": cfg.chat_size,
                              "conversations": cfg.conversations, "top_k": cfg.top_k,
                      **({"cutoffs": sorted(cfg.cutoffs)} if cfg.cutoffs else {})},
                       status="running")
    done = store.done_ids()
    failed_projects: list[str] = []
    all_qids: list[str] = []
    counter = {"n": 0}
    lock = threading.Lock()

    for conv_idx in cfg.conversations:
        if conv_idx >= len(dataset):
            log.warning("conversation %d out of range (%d rows)", conv_idx, len(dataset))
            continue
        rec = dataset[conv_idx]
        key = f"beam_{cfg.chat_size}_conv{conv_idx}"
        questions = extract_probing_questions(rec)
        if cfg.max_questions is not None:
            questions = questions[: cfg.max_questions]
        qids = [f"conv{conv_idx}_q{qi}" for qi in range(len(questions))]
        all_qids.extend(qids)
        pending = [(qi, q) for qi, q in enumerate(questions)
                   if f"conv{conv_idx}_q{qi}" not in done]
        if not pending:
            continue

        try:
            backend.init_project(key)
            for bi, turns in enumerate(parse_beam_chat(rec.get("chat", [])), 1):
                md = render_batch(bi, turns)
                backend.write_session(key, f"batch_{bi:04d}", md)
            backend.compile(key)
        except QuotaExhausted:
            raise  # provider wall: stop the run, do not fabricate failed questions
        except (CompileError, subprocess.TimeoutExpired) as exc:
            log.error("compile failed for %s: %s", key, exc)
            failed_projects.append(key)
            for qi, q in pending:
                qid = f"conv{conv_idx}_q{qi}"
                store.save(qid, {
                    "question_id": qid, "conversation_idx": conv_idx,
                    "group": q.get("question_type", "unknown"),
                    "question": q.get("question") or q.get("question_text") or "",
                    "status": "infra_error", "error": f"compile: {exc}",
                    "score": 0.0, "judge_parse_error": False,
                })
            continue

        def work(item):
            qi, q = item
            record = _process_question(cfg, backend, answerer, judge_llm,
                                       key, conv_idx, qi, q)
            store.save(record["question_id"], record)
            with lock:
                counter["n"] += 1
                heartbeat(log, counter["n"], len(all_qids), every=cfg.heartbeat_every)
            return record

        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.workers) as pool:
                list(pool.map(work, pending))
        except QuotaExhausted as exc:
            msg = (f"{exc} Completed questions are checkpointed in "
                   f"{cfg.out_dir}; resume with --project-name "
                   f"{cfg.project_name} once quota is available.")
            log.error("aborting run: %s", msg)
            write_run_metadata(cfg.out_dir, status="aborted_quota", error=msg,
                               rate_limit=GLOBAL_GATE.stats())
            raise

    rows = [r for r in store.load_all() if r.get("question_id") in set(all_qids)]
    metrics = aggregate_beam(rows)
    metrics_by_cutoff = None
    if cfg.cutoffs:
        metrics_by_cutoff = {}
        for c in sorted(cfg.cutoffs):
            label = f"top_{c}"
            views = [{**r, "score": r.get("cutoff_results", {}).get(label, {}).get(
                "score", r.get("score", 0.0))} for r in rows]
            metrics_by_cutoff[label] = aggregate_beam(views)
    aggregate = {
        "metadata": {
            "benchmark": "beam",
            "project_name": cfg.project_name,
            "timestamp": datetime.now().strftime("%Y%m%d_%H%M%S"),
            "binary_version": backend.binary_version(),
            "models": {"answerer": getattr(answerer, "model", "?"),
                       "judge": getattr(judge_llm, "model", "?")},
            "scope": {"chat_size": cfg.chat_size,
                      "conversations": cfg.conversations, "top_k": cfg.top_k},
            "total_questions": len(rows),
            "judge_parse_errors": sum(1 for r in rows if r.get("judge_parse_error")),
            "rate_limit": GLOBAL_GATE.stats(),
            "failed_projects": failed_projects,
            "usage": {"answerer": answerer.usage(), "judge": judge_llm.usage()},
        },
        "metrics": metrics,
        **({"metrics_by_cutoff": metrics_by_cutoff} if metrics_by_cutoff else {}),
        "latency": latency_stats(rows),
        "per_question": [
            {**{k: v for k, v in r.items() if k != "retrieval"},
             "retrieved_count": len(r.get("retrieval", []))}
            for r in rows
        ],
    }
    cfg.results_dir.mkdir(parents=True, exist_ok=True)
    out = cfg.results_dir / f"beam_{cfg.project_name}.json"
    out.write_text(json.dumps(aggregate, ensure_ascii=False, indent=1), encoding="utf-8")
    write_run_metadata(cfg.out_dir, status="complete", results_file=str(out),
                       failed_projects=failed_projects)
    log.info("results written to %s", out)
    return aggregate


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def parse_range(spec: str) -> list[int]:
    out: list[int] = []
    for part in spec.split(","):
        part = part.strip()
        if "-" in part:
            lo, hi = part.split("-", 1)
            out.extend(range(int(lo), int(hi) + 1))
        elif part:
            out.append(int(part))
    return out


def main() -> None:
    parser = argparse.ArgumentParser(description="BEAM on sage-wiki")
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--binary", default=None)
    parser.add_argument("--answerer-model", default="gpt-4o-mini")
    parser.add_argument("--judge-model", default="gpt-4o-mini")
    parser.add_argument("--compile-model", default="gpt-4o-mini")
    parser.add_argument("--chat-sizes", default="100K")
    parser.add_argument("--conversations", default="0-2")
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--top-k-cutoffs", default=None,
                        help="parity mode: comma-separated cutoffs (search at max, judge per cutoff)")
    parser.add_argument("--projects-dir", default=None,
                        help="reuse compiled projects from another run's projects directory")
    parser.add_argument("--max-questions", type=int, default=None,
                        help="cap questions per conversation")
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--smoke", action="store_true",
                        help="conversation 0 only, at most 2 questions")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(name)s %(levelname)s %(message)s")
    require_api_key()

    size = args.chat_sizes.split(",")[0]
    conversations = [0] if args.smoke else parse_range(args.conversations)
    cfg = RunConfig(
        out_dir=PKG_ROOT / "runs" / "beam" / args.project_name,
        results_dir=PKG_ROOT / "results",
        project_name=args.project_name,
        chat_size=size,
        conversations=conversations,
        top_k=args.top_k,
        cutoffs=[int(c) for c in args.top_k_cutoffs.split(",")] if args.top_k_cutoffs else None,
        max_questions=2 if args.smoke else args.max_questions,
        workers=args.workers,
    )
    backend = SageWikiBackend(
        binary=args.binary,
        root=Path(args.projects_dir) if args.projects_dir
        else PKG_ROOT / "runs" / "beam" / args.project_name / "projects",
        model=args.compile_model)
    answerer = LLMClient(model=args.answerer_model, role="answerer", workers=args.workers)
    judge = LLMClient(model=args.judge_model, role="judge", workers=args.workers)
    dataset = load_dataset(size)

    agg = run_benchmark(cfg, backend, answerer, judge, dataset)
    overall = agg["metrics"]["overall"]
    print(f"\nBEAM ({size}) overall avg score: {overall['avg_score']:.1f}% "
          f"({overall['total']} questions, infra_errors={overall['infra_errors']})")
    for group, m in agg["metrics"]["by_group"].items():
        extra = f", with_tau={m['score_with_tau']:.1f}%" if "score_with_tau" in m else ""
        print(f"  {group}: {m['avg_score']:.1f}%{extra}")


if __name__ == "__main__":
    main()
