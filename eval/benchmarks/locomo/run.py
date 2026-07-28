"""LOCOMO benchmark runner with sage-wiki as the memory backend.

Pipeline design follows mem0ai/memory-benchmarks (Apache-2.0, see NOTICE);
the Mem0 client is replaced by SageWikiBackend: one sage-wiki project per
conversation, sessions as markdown sources, `compile` for ingestion,
hybrid `search` for retrieval.

Usage (from the eval/ directory, repo binary built):
    python -m benchmarks.locomo.run --project-name my-run [--smoke]
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
import time
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path

from benchmarks.common.checkpoints import CheckpointStore, heartbeat, write_run_metadata
from benchmarks.common.llm import LLMClient, LLMError
from benchmarks.common.metrics import aggregate_accuracy, latency_stats
from benchmarks.common.ratelimit import GLOBAL_GATE, QuotaExhausted
from benchmarks.common.sagewiki import (
    CompileError,
    DegradedSearchError,
    SageWikiBackend,
    require_api_key,
)

from .prompts import (
    CATEGORIES_TO_EVALUATE,
    CATEGORY_NAMES,
    JUDGE_SYSTEM_PROMPT,
    get_answer_generation_prompt,
    get_judge_prompt,
    preprocess_answer,
)

log = logging.getLogger("locomo")

DATASET_URL = "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json"
PKG_ROOT = Path(__file__).resolve().parents[1]  # eval/benchmarks/
DEFAULT_DATASET = PKG_ROOT / "runs" / "datasets" / "locomo" / "locomo10.json"


# ---------------------------------------------------------------------------
# Dataset + rendering
# ---------------------------------------------------------------------------


def load_dataset(path: Path) -> list[dict]:
    if not path.is_file():
        path.parent.mkdir(parents=True, exist_ok=True)
        log.info("downloading LOCOMO-10 dataset to %s", path)
        urllib.request.urlretrieve(DATASET_URL, path)
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, list) or len(data) != 10:
        raise ValueError(f"invalid LOCOMO dataset at {path}: expected 10 conversations")
    return data


def parse_locomo_date(date_str: str):
    for fmt in ("%I:%M %p on %d %B, %Y", "%I:%M %p on %d %b, %Y"):
        try:
            return datetime.strptime(date_str, fmt)
        except (ValueError, TypeError):
            continue
    return None


def sorted_sessions(conversation: dict) -> list[tuple[str, str, list[dict]]]:
    keys = [k for k in conversation if re.match(r"^session_\d+$", k)]
    paired = [(k, conversation.get(f"{k}_date_time", ""), conversation[k]) for k in keys]

    def sort_key(item):
        parsed = parse_locomo_date(item[1])
        if parsed:
            return (0, parsed)
        return (1, datetime(2000, 1, int(re.search(r"\d+", item[0]).group())))

    paired.sort(key=sort_key)
    return paired


def _turn_text(turn: dict) -> str:
    """Turn text with mem0's image rendering (blip caption / query)."""
    text = turn.get("text", "") or ""
    blip = turn.get("blip_caption", "") or ""
    query = turn.get("query", "") or ""
    if query and blip:
        tag = f"[Sharing image - query: {query}. The image shows: {blip}]"
    elif query:
        tag = f"[Sharing image - query for: {query}]"
    elif blip:
        tag = f"[Sharing image that shows: {blip}]"
    else:
        tag = ""
    if tag:
        text = f"{text} {tag}" if text else tag
    return text


def render_session(session_key: str, date_str: str, turns: list[dict],
                   speaker_a: str, speaker_b: str) -> str:
    """One markdown source file per session. The session date sits in the
    heading AND the body so it survives into compiled summaries/articles —
    the only route dates have into search results (spec §3: no created_at)."""
    lines = [
        f"# Conversation session on {date_str}",
        "",
        f"This session took place on {date_str}. It is part of an ongoing "
        f"conversation between {speaker_a} and {speaker_b}.",
        "",
    ]
    for turn in turns:
        text = _turn_text(turn)
        if not text:
            continue
        lines.append(f"**{turn.get('speaker', '?')}:** {text}")
        lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Sampling
# ---------------------------------------------------------------------------


def stratified_question_sample(dataset: list[dict], conversations: list[int],
                               categories: list[int], n: int,
                               seed: int = 42) -> list[tuple[int, int, dict]]:
    """Draw n questions that mirror the full set's category mix.

    Proportional allocation with largest-remainder (so the parts sum to exactly
    n), and within each category the draw is spread round-robin across
    conversations — a plain random draw of 150 from 1,540 can silently
    over-weight one conversation, and conversation is a strong confound here
    (per-conversation accuracy ranges 44.7%-86.9%).

    Deterministic for a given seed so a sampled run is reproducible and two
    configurations can be compared on the identical subset.
    """
    pool: dict[int, list[tuple[int, int, dict]]] = {}
    for conv_idx in conversations:
        if conv_idx >= len(dataset):
            continue
        for qi, qa in enumerate(dataset[conv_idx].get("qa", [])):
            if qa.get("category") in categories:
                pool.setdefault(qa["category"], []).append((conv_idx, qi, qa))

    total = sum(len(v) for v in pool.values())
    if n >= total:
        return sorted((item for items in pool.values() for item in items),
                      key=lambda t: (t[0], t[1]))

    # Largest-remainder allocation across categories.
    exact = {cat: len(items) * n / total for cat, items in pool.items()}
    alloc = {cat: int(v) for cat, v in exact.items()}
    for cat in sorted(pool, key=lambda c: (-(exact[c] - alloc[c]), c)):
        if sum(alloc.values()) >= n:
            break
        alloc[cat] += 1

    rng = random.Random(seed)
    picked: list[tuple[int, int, dict]] = []
    for cat in sorted(pool):
        want = alloc.get(cat, 0)
        if want <= 0:
            continue
        # Group this category's questions by conversation, shuffle within each,
        # then take round-robin so the draw spans conversations evenly.
        by_conv: dict[int, list[tuple[int, int, dict]]] = {}
        for item in pool[cat]:
            by_conv.setdefault(item[0], []).append(item)
        for items in by_conv.values():
            rng.shuffle(items)
        order = sorted(by_conv)
        rng.shuffle(order)
        taken, idx = 0, 0
        while taken < want:
            progressed = False
            for conv in order:
                if taken >= want:
                    break
                bucket = by_conv[conv]
                if idx < len(bucket):
                    picked.append(bucket[idx])
                    taken += 1
                    progressed = True
            if not progressed:
                break
            idx += 1
    return sorted(picked, key=lambda t: (t[0], t[1]))


# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------


@dataclass
class RunConfig:
    out_dir: Path
    results_dir: Path
    project_name: str
    conversations: list[int] = field(default_factory=lambda: list(range(10)))
    categories: list[int] = field(default_factory=lambda: list(CATEGORIES_TO_EVALUATE))
    top_k: int = 10
    cutoffs: list[int] | None = None  # parity mode: search at max, judge per cutoff
    sample: int | None = None         # stratified subset of the in-scope questions
    sample_seed: int = 42
    max_questions: int | None = None
    workers: int = 4
    heartbeat_every: int = 25


def _judge(judge_llm, category: int, question: str, ground_truth: str,
           generated: str) -> tuple[str, float, bool, str]:
    """Binary judge; None from generate_json (parse failure after its own
    retries) → WRONG + judge_parse_error (spec §5)."""
    processed = preprocess_answer(category, str(ground_truth))
    prompt = get_judge_prompt(category, question, processed, generated)
    raw = judge_llm.generate_json(JUDGE_SYSTEM_PROMPT, prompt)
    if not isinstance(raw, dict):
        return "WRONG", 0.0, True, ""
    correct = str(raw.get("label", "")).upper() == "CORRECT"
    return ("CORRECT" if correct else "WRONG"), (1.0 if correct else 0.0), False, \
        str(raw.get("reasoning", ""))


def _process_question(cfg: RunConfig, backend, answerer, judge_llm,
                      conv_idx: int, qi: int, qa: dict, ref_date: str) -> dict:
    qid = f"conv{conv_idx}_q{qi}"
    category = qa["category"]
    record = {
        "question_id": qid,
        "conversation_idx": conv_idx,
        "category": category,
        "group": CATEGORY_NAMES.get(category, "unknown"),
        "question": qa["question"],
        "ground_truth": str(qa["answer"]),
        "status": "ok",
        "judge_parse_error": False,
    }
    limit = max(cfg.cutoffs) if cfg.cutoffs else cfg.top_k
    try:
        resp = backend.search(f"conv{conv_idx}", qa["question"], limit=limit)
        record["search_latency_ms"] = resp.latency_ms
        record["retrieval"] = resp.results

        if cfg.cutoffs:
            # Parity mode (mem0 pipeline shape): answer + judge at each cutoff.
            cutoff_results: dict[str, dict] = {}
            any_parse_err = False
            for c in sorted(cfg.cutoffs):
                sliced = resp.results[:c]
                gen_prompt = get_answer_generation_prompt(
                    qa["question"], sliced, reference_date=ref_date)
                answer = answerer.generate("", gen_prompt)
                if "ANSWER:" in answer:
                    answer = answer.rsplit("ANSWER:", 1)[-1].strip()
                judgment, score, parse_err, reason = _judge(
                    judge_llm, category, qa["question"], qa["answer"], answer)
                any_parse_err = any_parse_err or parse_err
                cutoff_results[f"top_{c}"] = {
                    "judgment": judgment, "score": score,
                    "generated_answer": answer,
                    "memories_evaluated": len(sliced),
                    "judge_reason": reason,
                }
            record.update(cutoff_results=cutoff_results,
                          judge_parse_error=any_parse_err,
                          score=cutoff_results[f"top_{max(cfg.cutoffs)}"]["score"])
            return record

        gen_prompt = get_answer_generation_prompt(qa["question"], resp.results,
                                                  reference_date=ref_date)
        answer = answerer.generate("", gen_prompt)
        if "ANSWER:" in answer:
            answer = answer.rsplit("ANSWER:", 1)[-1].strip()
        record["generated_answer"] = answer
        judgment, score, parse_err, reason = _judge(
            judge_llm, category, qa["question"], qa["answer"], answer)
    except QuotaExhausted:
        raise  # provider wall: stop the run, do not fabricate a failed question
    except (DegradedSearchError, LLMError, subprocess.TimeoutExpired) as exc:
        record.update(status="infra_error", error=str(exc), score=0.0)
        record.setdefault("search_latency_ms", None)
        return record

    record.update(judgment=judgment, score=score,
                  judge_parse_error=parse_err, judge_reason=reason)
    return record


def run_benchmark(cfg: RunConfig, backend, answerer, judge_llm,
                  dataset: list[dict]) -> dict:
    store = CheckpointStore(cfg.out_dir)
    write_run_metadata(cfg.out_dir, benchmark="locomo", project_name=cfg.project_name,
                       binary_version=backend.binary_version(),
                       models={"answerer": getattr(answerer, "model", "?"),
                               "judge": getattr(judge_llm, "model", "?")},
                       scope={"conversations": cfg.conversations,
                              "categories": cfg.categories, "top_k": cfg.top_k},
                       status="running")
    done = store.done_ids()
    compile_stats: dict[str, dict] = {}
    failed_projects: list[str] = []
    all_qids: list[str] = []
    counter = {"n": 0}
    counter_lock = threading.Lock()

    allowed: set[tuple[int, int]] | None = None
    if cfg.sample is not None:
        allowed = {(c, qi) for c, qi, _ in stratified_question_sample(
            dataset, cfg.conversations, cfg.categories, cfg.sample, cfg.sample_seed)}
        log.info("stratified sample: %d questions across %d conversations",
                 len(allowed), len({c for c, _ in allowed}))

    def scoped_questions(entry: dict) -> list[tuple[int, dict]]:
        conv_idx = dataset.index(entry)
        qs = [(qi, qa) for qi, qa in enumerate(entry.get("qa", []))
              if qa.get("category") in cfg.categories
              and (allowed is None or (conv_idx, qi) in allowed)]
        return qs[: cfg.max_questions] if cfg.max_questions is not None else qs

    run_total = sum(len(scoped_questions(dataset[c])) for c in cfg.conversations)

    for conv_idx in cfg.conversations:
        entry = dataset[conv_idx]
        conversation = entry["conversation"]
        key = f"conv{conv_idx}"
        sessions = sorted_sessions(conversation)
        questions = scoped_questions(entry)
        pending = [(qi, qa) for qi, qa in questions
                   if f"conv{conv_idx}_q{qi}" not in done]
        all_qids.extend(f"conv{conv_idx}_q{qi}" for qi, _ in questions)

        # -- ingest + compile (skipped when already compiled) ------------------
        try:
            if pending:
                backend.init_project(key)
                for skey, date_str, turns in sessions:
                    backend.write_session(key, skey, render_session(
                        skey, date_str, turns,
                        conversation["speaker_a"], conversation["speaker_b"]))
                stats = backend.compile(key)
                compile_stats[key] = {"seconds": stats.seconds,
                                      "vector_count": stats.vector_count,
                                      "sources": stats.source_count,
                                      "skipped": stats.skipped}
        except (CompileError, subprocess.TimeoutExpired) as exc:
            log.error("compile failed for %s: %s", key, exc)
            failed_projects.append(key)
            for qi, qa in pending:
                qid = f"conv{conv_idx}_q{qi}"
                store.save(qid, {
                    "question_id": qid, "conversation_idx": conv_idx,
                    "category": qa["category"],
                    "group": CATEGORY_NAMES.get(qa["category"], "unknown"),
                    "question": qa["question"], "ground_truth": str(qa["answer"]),
                    "status": "infra_error", "error": f"compile: {exc}",
                    "score": 0.0, "judge_parse_error": False,
                })
            continue

        ref_date = sessions[-1][1] if sessions else None

        def work(item):
            qi, qa = item
            record = _process_question(cfg, backend, answerer, judge_llm,
                                       conv_idx, qi, qa, ref_date)
            store.save(record["question_id"], record)  # only reached on success
            with counter_lock:
                counter["n"] += 1
                heartbeat(log, counter["n"], run_total, every=cfg.heartbeat_every)
            return record

        if pending:
            try:
                with concurrent.futures.ThreadPoolExecutor(max_workers=cfg.workers) as pool:
                    list(pool.map(work, pending))
            except QuotaExhausted as exc:
                # The runner owns the resume guidance: it knows the project
                # name, and an abort raised anywhere must leave the operator
                # a way back in.
                msg = (f"{exc} Completed questions are checkpointed in "
                       f"{cfg.out_dir}; resume with --project-name "
                       f"{cfg.project_name} once quota is available.")
                log.error("aborting run: %s", msg)
                write_run_metadata(cfg.out_dir, status="aborted_quota",
                                   error=msg,
                                   rate_limit=GLOBAL_GATE.stats(),
                                   failed_projects=failed_projects)
                raise

    # -- aggregate -------------------------------------------------------------
    rows = [r for r in store.load_all() if r.get("question_id") in set(all_qids)]
    metrics = aggregate_accuracy(rows)
    metrics_by_cutoff = None
    if cfg.cutoffs:
        metrics_by_cutoff = {}
        for c in sorted(cfg.cutoffs):
            label = f"top_{c}"
            views = [
                {**r, "score": r.get("cutoff_results", {}).get(label, {}).get(
                    "score", r.get("score", 0.0))}
                for r in rows
            ]
            metrics_by_cutoff[label] = aggregate_accuracy(views)
    latency = latency_stats(rows)
    usage = {"answerer": answerer.usage(), "judge": judge_llm.usage()}
    aggregate = {
        "metadata": {
            "benchmark": "locomo",
            "project_name": cfg.project_name,
            "timestamp": datetime.now().strftime("%Y%m%d_%H%M%S"),
            "binary_version": backend.binary_version(),
            "models": {"answerer": getattr(answerer, "model", "?"),
                       "judge": getattr(judge_llm, "model", "?")},
            "scope": {"conversations": cfg.conversations,
                      "categories": cfg.categories, "top_k": cfg.top_k,
                      **({"cutoffs": sorted(cfg.cutoffs)} if cfg.cutoffs else {}),
                      **({"sample": cfg.sample, "sample_seed": cfg.sample_seed}
                         if cfg.sample is not None else {})},
            "total_questions": len(rows),
            "judge_parse_errors": sum(1 for r in rows if r.get("judge_parse_error")),
            "compile_stats": compile_stats,
            "failed_projects": failed_projects,
            "rate_limit": GLOBAL_GATE.stats(),
            "usage": usage,
        },
        "metrics": metrics,
        **({"metrics_by_cutoff": metrics_by_cutoff} if metrics_by_cutoff else {}),
        "latency": latency,
        "per_question": [
            {**{k: v for k, v in r.items() if k != "retrieval"},
             "retrieved_count": len(r.get("retrieval", []))}
            for r in rows
        ],
    }
    cfg.results_dir.mkdir(parents=True, exist_ok=True)
    out = cfg.results_dir / f"locomo_{cfg.project_name}.json"
    out.write_text(json.dumps(aggregate, ensure_ascii=False, indent=1), encoding="utf-8")
    write_run_metadata(cfg.out_dir, status="complete", results_file=str(out),
                       usage=usage, failed_projects=failed_projects)
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
    parser = argparse.ArgumentParser(description="LOCOMO on sage-wiki")
    parser.add_argument("--project-name", required=True)
    parser.add_argument("--binary", default=None)
    parser.add_argument("--answerer-model", default="gpt-4o-mini")
    parser.add_argument("--judge-model", default="gpt-4o-mini")
    parser.add_argument("--compile-model", default="gpt-4o-mini")
    parser.add_argument("--conversations", default="0-9")
    parser.add_argument("--categories", default="1,2,3,4")
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--top-k-cutoffs", default=None,
                        help="parity mode: comma-separated cutoffs (search at max, judge per cutoff)")
    parser.add_argument("--projects-dir", default=None,
                        help="reuse compiled projects from another run's projects directory")
    parser.add_argument("--sample", type=int, default=None,
                        help="evaluate a stratified random subset of N questions "
                             "(category mix preserved, spread across conversations)")
    parser.add_argument("--sample-seed", type=int, default=42)
    parser.add_argument("--max-questions", type=int, default=None)
    parser.add_argument("--workers", type=int, default=4)
    parser.add_argument("--smoke", action="store_true",
                        help="conversation 0 only, at most 5 questions")
    parser.add_argument("--dataset-path", default=str(DEFAULT_DATASET))
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(name)s %(levelname)s %(message)s")
    require_api_key()

    conversations = [0] if args.smoke else parse_range(args.conversations)
    max_q = 5 if args.smoke else args.max_questions

    cfg = RunConfig(
        out_dir=PKG_ROOT / "runs" / "locomo" / args.project_name,
        results_dir=PKG_ROOT / "results",
        project_name=args.project_name,
        conversations=conversations,
        categories=[int(c) for c in args.categories.split(",")],
        top_k=args.top_k,
        cutoffs=[int(c) for c in args.top_k_cutoffs.split(",")] if args.top_k_cutoffs else None,
        sample=args.sample,
        sample_seed=args.sample_seed,
        max_questions=max_q,
        workers=args.workers,
    )
    backend = SageWikiBackend(binary=args.binary,
                              root=Path(args.projects_dir) if args.projects_dir
                              else PKG_ROOT / "runs" / "locomo" / args.project_name / "projects",
                              model=args.compile_model)
    answerer = LLMClient(model=args.answerer_model, role="answerer", workers=args.workers)
    judge = LLMClient(model=args.judge_model, role="judge", workers=args.workers)
    dataset = load_dataset(Path(args.dataset_path))

    agg = run_benchmark(cfg, backend, answerer, judge, dataset)
    overall = agg["metrics"]["overall"]
    print(f"\nLOCOMO overall: {overall['correct']}/{overall['total']} "
          f"({overall['accuracy']:.1f}%), infra_errors={overall['infra_errors']}")
    for group, m in agg["metrics"]["by_group"].items():
        print(f"  {group}: {m['correct']}/{m['total']} ({m['accuracy']:.1f}%)")


if __name__ == "__main__":
    main()
