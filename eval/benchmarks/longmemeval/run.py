"""LongMemEval benchmark runner with sage-wiki as the memory backend.

Pipeline design follows mem0ai/memory-benchmarks (Apache-2.0, see NOTICE).
Each question owns a haystack of ~40-60 chat sessions; every question gets
its own sage-wiki project (compile cost is the scope driver — default is a
stratified sample of --per-type questions per question type, seed 42).

Usage (from the eval/ directory):
    python -m benchmarks.longmemeval.run --project-name my-run --per-type 5
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import logging
import random
import re
import subprocess
import threading
import urllib.request
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

from benchmarks.common.checkpoints import CheckpointStore, heartbeat, write_run_metadata
from benchmarks.common.llm import LLMClient, LLMError
from benchmarks.common.ratelimit import GLOBAL_GATE, QuotaExhausted
from benchmarks.common.metrics import aggregate_accuracy, latency_stats
from benchmarks.common.sagewiki import (
    CompileError,
    DegradedSearchError,
    SageWikiBackend,
    require_api_key,
)

from .prompts import QUESTION_TYPES, get_answer_generation_prompt, get_judge_prompt

log = logging.getLogger("longmemeval")

DATASET_URL = ("https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/"
               "resolve/main/longmemeval_s_cleaned.json")
PKG_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_DATASET = PKG_ROOT / "runs" / "datasets" / "longmemeval" / "longmemeval_s_cleaned.json"
JUDGE_RETRIES = 3


# ---------------------------------------------------------------------------
# Dataset, sampling, rendering
# ---------------------------------------------------------------------------


def load_dataset(path: Path) -> list[dict]:
    if not path.is_file():
        path.parent.mkdir(parents=True, exist_ok=True)
        log.info("downloading LongMemEval-S (~265MB) to %s", path)
        urllib.request.urlretrieve(DATASET_URL, path)
    return json.loads(path.read_text(encoding="utf-8"))


def stratified_sample(questions: list[dict], per_type: int, seed: int = 42,
                      type_filter: list[str] | None = None) -> list[dict]:
    """Upstream parity: group by type, sort by question_id, rng.sample per type."""
    types = type_filter or QUESTION_TYPES
    groups: dict[str, list[dict]] = {}
    for q in questions:
        if q["question_type"] in types:
            groups.setdefault(q["question_type"], []).append(q)
    for qtype in groups:
        groups[qtype].sort(key=lambda q: q["question_id"])
    rng = random.Random(seed)
    sampled: list[dict] = []
    for qtype in sorted(groups):
        group = groups[qtype]
        sampled.extend(rng.sample(group, min(per_type, len(group))))
    sampled.sort(key=lambda q: q["question_id"])
    return sampled


def parse_lme_date(date_str: str):
    try:
        cleaned = re.sub(r"\s*\([A-Za-z]+\)\s*", " ", date_str).strip()
        return datetime.strptime(cleaned, "%Y/%m/%d %H:%M")
    except (ValueError, TypeError):
        return None


def human_date(date_str: str) -> str:
    parsed = parse_lme_date(date_str)
    return parsed.strftime("%A, %B %d, %Y") if parsed else date_str


def sorted_sessions(question: dict) -> list[tuple[str, str, list[dict]]]:
    paired = list(zip(question["haystack_session_ids"], question["haystack_dates"],
                      question["haystack_sessions"]))

    def key(item):
        parsed = parse_lme_date(item[1])
        return (0, parsed, item[1]) if parsed else (1, datetime.min, item[1])

    paired.sort(key=key)
    return paired


def render_session(session_id: str, date_str: str, turns: list[dict]) -> str:
    """Date lands in heading AND body — the only route dates have into search
    results (spec §3: no created_at on sage-wiki results)."""
    lines = [
        f"# Chat session on {date_str}",
        "",
        f"This chat session took place on {date_str}.",
        "",
    ]
    for turn in turns:
        content = (turn.get("content") or "").strip()
        if not content:
            continue
        role = "User" if turn.get("role") == "user" else "Assistant"
        lines.append(f"**{role}:** {content}")
        lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Judge (yes/no, upstream parse parity + parse-error flag)
# ---------------------------------------------------------------------------


def parse_yes_no(raw: str) -> tuple[bool, bool]:
    """(verdict, parse_error). Verdict logic is the upstream port; parse_error
    is True when no yes/no token exists anywhere (verdict then defaulted)."""
    text = (raw or "").strip()
    if not text:
        return False, True
    after_cot = re.split(r"</judge_thinking>|</thinking>", text, flags=re.IGNORECASE)
    region = after_cot[-1].strip() if after_cot else text
    lines = [ln.strip().lower() for ln in region.splitlines() if ln.strip()]
    for line in reversed(lines):
        if line == "yes":
            return True, False
        if line == "no":
            return False, False
    tokens = re.findall(r"\b(yes|no)\b", region.lower())
    if tokens:
        return tokens[-1] == "yes", False
    return text.lower().startswith("yes"), True


# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------


@dataclass
class RunConfig:
    out_dir: Path
    results_dir: Path
    project_name: str
    per_type: int = 5
    seed: int = 42
    top_k: int = 10
    cutoffs: list[int] | None = None  # parity mode: search at max, judge per cutoff
    max_questions: int | None = None
    workers: int = 2
    heartbeat_every: int = 25


def _answer_and_judge(cfg, answerer, judge_llm, q, memories, qdate):
    """Answer from `memories`, then judge. Returns (judgment, score, parse_error, answer)."""
    gen_prompt = get_answer_generation_prompt(q["question"], memories, qdate)
    answer = answerer.generate("", gen_prompt)
    answer = re.sub(r"[<\[]mem_thinking[>\]].*?[<\[]/mem_thinking[>\]]", "",
                    answer, flags=re.DOTALL).strip()
    if "ANSWER:" in answer:
        answer = answer.rsplit("ANSWER:", 1)[-1].strip()

    judge_prompt = get_judge_prompt(
        question_type=q["question_type"], question_id=q["question_id"],
        question=q["question"], answer=str(q["answer"]), response=answer,
        question_date=qdate)
    verdict = parse_error = None
    for _ in range(JUDGE_RETRIES):
        raw = judge_llm.generate("", judge_prompt)
        verdict, parse_error = parse_yes_no(raw)
        if not parse_error:
            break
    # A parse failure is never a PASS (spec §5).
    correct = bool(verdict) and not parse_error
    return ("PASS" if correct else "FAIL"), (1.0 if correct else 0.0), bool(parse_error), answer


def _process_question(cfg: RunConfig, backend, answerer, judge_llm, q: dict) -> dict:
    qid = q["question_id"]
    qdate = human_date(q.get("question_date", ""))
    record = {
        "question_id": qid,
        "group": q["question_type"],
        "question": q["question"],
        "ground_truth": str(q["answer"]),
        "question_date": q.get("question_date", ""),
        "is_abstention": qid.endswith("_abs"),
        "status": "ok",
        "judge_parse_error": False,
    }
    key = f"lme_{qid}"
    try:
        backend.init_project(key)
        for sid, date_str, turns in sorted_sessions(q):
            backend.write_session(key, sid, render_session(sid, date_str, turns))
        backend.compile(key)
    except QuotaExhausted:
        raise
    except (CompileError, subprocess.TimeoutExpired) as exc:
        record.update(status="infra_error", error=f"compile: {exc}", score=0.0,
                      search_latency_ms=None, failed_project=key)
        return record
    limit = max(cfg.cutoffs) if cfg.cutoffs else cfg.top_k
    try:
        resp = backend.search(key, q["question"], limit=limit)
        record["search_latency_ms"] = resp.latency_ms
        record["retrieval"] = resp.results

        if cfg.cutoffs:
            cutoff_results: dict[str, dict] = {}
            any_parse_err = False
            for c in sorted(cfg.cutoffs):
                sliced = resp.results[:c]
                judgment, score, parse_err, answer = _answer_and_judge(
                    cfg, answerer, judge_llm, q, sliced, qdate)
                any_parse_err = any_parse_err or parse_err
                cutoff_results[f"top_{c}"] = {
                    "judgment": judgment, "score": score,
                    "generated_answer": answer, "memories_evaluated": len(sliced),
                }
            record.update(cutoff_results=cutoff_results,
                          judge_parse_error=any_parse_err,
                          score=cutoff_results[f"top_{max(cfg.cutoffs)}"]["score"])
            return record

        judgment, score, parse_err, answer = _answer_and_judge(
            cfg, answerer, judge_llm, q, resp.results, qdate)
        record.update(generated_answer=answer, judgment=judgment, score=score,
                      judge_parse_error=parse_err)
        return record
    except QuotaExhausted:
        raise  # provider wall: stop the run, do not fabricate a failed question
    except (DegradedSearchError, LLMError, subprocess.TimeoutExpired) as exc:
        record.update(status="infra_error", error=str(exc), score=0.0)
        record.setdefault("search_latency_ms", None)
        return record



def run_benchmark(cfg: RunConfig, backend, answerer, judge_llm,
                  dataset: list[dict]) -> dict:
    store = CheckpointStore(cfg.out_dir)
    questions = stratified_sample(dataset, cfg.per_type, cfg.seed)
    if cfg.max_questions is not None:
        questions = questions[: cfg.max_questions]
    write_run_metadata(cfg.out_dir, benchmark="longmemeval",
                       project_name=cfg.project_name,
                       binary_version=backend.binary_version(),
                       models={"answerer": getattr(answerer, "model", "?"),
                               "judge": getattr(judge_llm, "model", "?")},
                       scope={"per_type": cfg.per_type, "seed": cfg.seed,
                              "top_k": cfg.top_k,
                              "question_ids": [q["question_id"] for q in questions]},
                       status="running")
    done = store.done_ids()
    pending = [q for q in questions if q["question_id"] not in done]
    counter = {"n": 0}
    lock = threading.Lock()

    def work(q):
        record = _process_question(cfg, backend, answerer, judge_llm, q)
        store.save(record["question_id"], record)
        with lock:
            counter["n"] += 1
            heartbeat(log, counter["n"], len(pending), every=cfg.heartbeat_every)
        return record

    if pending:
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

    wanted = {q["question_id"] for q in questions}
    rows = [r for r in store.load_all() if r.get("question_id") in wanted]
    failed_projects = sorted({r["failed_project"] for r in rows
                              if r.get("failed_project")})
    metrics = aggregate_accuracy(rows)
    metrics_by_cutoff = None
    if cfg.cutoffs:
        metrics_by_cutoff = {}
        for c in sorted(cfg.cutoffs):
            label = f"top_{c}"
            views = [{**r, "score": r.get("cutoff_results", {}).get(label, {}).get(
                "score", r.get("score", 0.0))} for r in rows]
            metrics_by_cutoff[label] = aggregate_accuracy(views)
    aggregate = {
        "metadata": {
            "benchmark": "longmemeval",
            "project_name": cfg.project_name,
            "timestamp": datetime.now().strftime("%Y%m%d_%H%M%S"),
            "binary_version": backend.binary_version(),
            "models": {"answerer": getattr(answerer, "model", "?"),
                       "judge": getattr(judge_llm, "model", "?")},
            "scope": {"per_type": cfg.per_type, "seed": cfg.seed, "top_k": cfg.top_k,
                      **({"cutoffs": sorted(cfg.cutoffs)} if cfg.cutoffs else {})},
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
    out = cfg.results_dir / f"longmemeval_{cfg.project_name}.json"
    out.write_text(json.dumps(aggregate, ensure_ascii=False, indent=1), encoding="utf-8")
    write_run_metadata(cfg.out_dir, status="complete", results_file=str(out),
                       failed_projects=failed_projects)
    log.info("results written to %s", out)
    return aggregate


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main() -> None:
    parser = argparse.ArgumentParser(description="LongMemEval on sage-wiki")
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--binary", default=None)
    parser.add_argument("--answerer-model", default="gpt-4o-mini")
    parser.add_argument("--judge-model", default="gpt-4o-mini")
    parser.add_argument("--compile-model", default="gpt-4o-mini")
    parser.add_argument("--per-type", type=int, default=5)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--top-k-cutoffs", default=None,
                        help="parity mode: comma-separated cutoffs (search at max, judge per cutoff)")
    parser.add_argument("--projects-dir", default=None,
                        help="reuse compiled projects from another run's projects directory")
    parser.add_argument("--max-questions", type=int, default=None)
    parser.add_argument("--workers", type=int, default=2,
                        help="concurrent questions (each compiles its own project)")
    parser.add_argument("--smoke", action="store_true", help="1 question only")
    parser.add_argument("--dataset-path", default=str(DEFAULT_DATASET))
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(name)s %(levelname)s %(message)s")
    require_api_key()

    cfg = RunConfig(
        out_dir=PKG_ROOT / "runs" / "longmemeval" / args.project_name,
        results_dir=PKG_ROOT / "results",
        project_name=args.project_name,
        per_type=args.per_type,
        seed=args.seed,
        top_k=args.top_k,
        cutoffs=[int(c) for c in args.top_k_cutoffs.split(",")] if args.top_k_cutoffs else None,
        max_questions=1 if args.smoke else args.max_questions,
        workers=args.workers,
    )
    backend = SageWikiBackend(
        binary=args.binary,
        root=Path(args.projects_dir) if args.projects_dir
        else PKG_ROOT / "runs" / "longmemeval" / args.project_name / "projects",
        model=args.compile_model)
    answerer = LLMClient(model=args.answerer_model, role="answerer", workers=args.workers)
    judge = LLMClient(model=args.judge_model, role="judge", workers=args.workers)
    dataset = load_dataset(Path(args.dataset_path))

    agg = run_benchmark(cfg, backend, answerer, judge, dataset)
    overall = agg["metrics"]["overall"]
    print(f"\nLongMemEval overall: {overall['correct']}/{overall['total']} "
          f"({overall['accuracy']:.1f}%), infra_errors={overall['infra_errors']}")
    for group, m in agg["metrics"]["by_group"].items():
        print(f"  {group}: {m['correct']}/{m['total']} ({m['accuracy']:.1f}%)")


if __name__ == "__main__":
    main()
