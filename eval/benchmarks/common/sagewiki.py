"""SageWikiBackend — drives the sage-wiki binary as the memory system under test.

One sage-wiki project directory per conversation/haystack (the user_id
isolation analogue). Ingestion = markdown session files in raw/ + `compile`;
retrieval = `search --format json` (hybrid BM25+vector RRF).

Degraded-search contract (spec §3): the binary silently falls back to
BM25-only on embed failure (stderr warning) and on config-load failure
(silent). Two guards catch this — the stderr scan and the result-shape guard
(non-empty results where no result has VectorRank > 0). Either → retry, then
DegradedSearchError; callers record the question as infra_error rather than
scoring a BM25-only answer as hybrid.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path

from benchmarks.common.ratelimit import GLOBAL_GATE

# Degrade markers, one per sage-wiki search generation. The pipeline rewrite in
# arch/search-upgrade replaced the original single-line warning with a slog
# record whose wording shares no substring with it, so both must be matched or
# the stderr guard silently goes blind against one of the two binaries.
EMBED_WARNING = "warning: embed failed, using BM25-only"          # pre-upgrade
EMBED_WARNING_V2 = "query embedding failed for every variant"     # post-upgrade
EMBED_WARNINGS = (EMBED_WARNING, EMBED_WARNING_V2)
RATE_LIMIT_MARKERS = ("rate limited", "http 429", "429", "quota")
SEARCH_RETRIES = 2        # retries after the initial attempt (permanent degrade)
SEARCH_RATE_RETRIES = 6   # a rate-limited embedder is transient — wait it out
CONFIG_TEMPLATE = """version: 1
project: {key}
description: "memory benchmark project"

sources:
  - path: raw
    type: auto
    watch: false

output: wiki

api:
  provider: openai
  api_key: ${{OPENAI_API_KEY}}
  rate_limit: 120

models:
  summarize: {model}
  extract: {model}
  write: {model}
  lint: {model}
  query: {model}

embed:
  provider: openai
  model: {embed_model}

compiler:
  mode: standard        # sync compile — auto mode batches at >=10 sources (async, breaks the pipeline)
  max_parallel: 4
  summary_max_tokens: 4000
  article_max_tokens: 4000
  auto_commit: false
  auto_lint: false

search:
  hybrid_weight_bm25: 0.7
  hybrid_weight_vector: 0.3
  default_limit: 10
"""


class CompileError(Exception):
    pass


class DegradedSearchError(Exception):
    """Search fell back to BM25-only (embed/config failure) and stayed degraded."""


@dataclass
class CompileStats:
    skipped: bool
    seconds: float
    vector_count: int
    source_count: int


@dataclass
class SearchResponse:
    results: list[dict]
    latency_ms: float
    raw: list[dict] = field(default_factory=list)


def default_binary() -> str:
    """SAGE_WIKI_BIN env, else the repo-root binary — anchored to this file, never cwd."""
    env = os.environ.get("SAGE_WIKI_BIN")
    if env:
        return env
    return str(Path(__file__).resolve().parents[3] / "sage-wiki")


def require_api_key() -> None:
    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY is not set. Export a funded OpenAI key and re-run.")


class SageWikiBackend:
    def __init__(self, binary: str | None = None, root: Path | str = "runs/projects",
                 model: str = "gpt-4o-mini", embed_model: str = "text-embedding-3-small",
                 gate=None):
        self.binary = binary or default_binary()
        self.root = Path(root)
        self.model = model
        self.embed_model = embed_model
        self.gate = gate if gate is not None else GLOBAL_GATE

    # -- setup -----------------------------------------------------------------

    def _check_binary(self) -> None:
        if not Path(self.binary).is_file():
            raise FileNotFoundError(
                f"sage-wiki binary not found at {self.binary}. "
                f"Build it from the repo root with: go build -o sage-wiki ./cmd/sage-wiki "
                f"(or set SAGE_WIKI_BIN)."
            )

    def project_dir(self, key: str) -> Path:
        return self.root / key

    def init_project(self, key: str) -> Path:
        self._check_binary()
        proj = self.project_dir(key)
        (proj / "raw").mkdir(parents=True, exist_ok=True)
        cfg = CONFIG_TEMPLATE.format(key=key, model=self.model, embed_model=self.embed_model)
        (proj / "config.yaml").write_text(cfg, encoding="utf-8")
        return proj

    def write_session(self, key: str, session_id: str, markdown: str) -> Path:
        path = self.project_dir(key) / "raw" / f"{session_id}.md"
        path.write_text(markdown, encoding="utf-8")
        return path

    # -- compile ---------------------------------------------------------------

    def _source_count(self, key: str) -> int:
        raw = self.project_dir(key) / "raw"
        return len(list(raw.glob("*.md"))) if raw.is_dir() else 0

    def _run(self, key: str, *args: str, timeout_s: int = 3600) -> subprocess.CompletedProcess:
        return subprocess.run(
            [self.binary, *args, "--project", str(self.project_dir(key))],
            capture_output=True, text=True, timeout=timeout_s,
        )

    def project_stats(self, key: str) -> dict:
        proc = self._run(key, "status", "--format", "json", timeout_s=120)
        try:
            data = json.loads(proc.stdout).get("data") or {}
        except json.JSONDecodeError:
            return {}
        return data

    def vector_count(self, key: str) -> int:
        return int(self.project_stats(key).get("vector_count") or 0)

    def compile(self, key: str, timeout_s: int = 7200) -> CompileStats:
        proj = self.project_dir(key)
        marker = proj / ".compiled"
        n_sources = self._source_count(key)
        if marker.is_file() and marker.read_text().strip() == str(n_sources):
            # Re-validate on resume — a marker written by a run with a weaker
            # predicate (or a corrupted project) must not skip the gate.
            stats = self.project_stats(key)
            compiled = int(stats.get("source_count") or 0)
            vc = int(stats.get("vector_count") or 0)
            if compiled < n_sources or vc < n_sources:
                raise CompileError(
                    f"resume for {key}: .compiled marker present but project is "
                    f"invalid (compiled source count {compiled}/{n_sources}, "
                    f"vectors {vc}) — delete the project directory and rerun."
                )
            return CompileStats(skipped=True, seconds=0.0,
                                vector_count=vc, source_count=n_sources)

        start = time.monotonic()
        log = proj / "compile.log"
        last = self._run(key, "compile", timeout_s=timeout_s)
        for attempt in range(2):  # initial + 1 retry (sage-wiki resumes its own checkpoints)
            with log.open("a", encoding="utf-8") as fh:
                fh.write(f"--- compile attempt {attempt + 1} (exit {last.returncode}) ---\n")
                fh.write(last.stdout)
                fh.write(last.stderr)
            if last.returncode == 0 or attempt == 1:
                break
            last = self._run(key, "compile", timeout_s=timeout_s)
        if last.returncode != 0:
            raise CompileError(
                f"compile failed for {key} after retry (exit {last.returncode}); "
                f"see {log}. stderr tail: {last.stderr[-300:]}"
            )

        # Hardened success predicate (C-1/M-1 regression): exit 0 is NOT proof of
        # work. An interrupted compile whose resume no-ops (leased queue items,
        # "0 summarized") exits 0 with the raw sources FTS-indexed and a stray
        # vector — passing a bare vector_count>0 check and then benchmarking raw
        # transcripts. Require the manifest's COMPILED source count to match the
        # raw file count, and at least one vector per source (healthy projects
        # run ~1.5-2x).
        stats = self.project_stats(key)
        compiled = int(stats.get("source_count") or 0)
        vc = int(stats.get("vector_count") or 0)
        if compiled < n_sources:
            raise CompileError(
                f"compile for {key} exited 0 but compiled source count is "
                f"{compiled} of {n_sources} raw sources — a no-op/interrupted "
                f"compile; refusing to run questions against uncompiled content. "
                f"Delete the project directory (incl. .compiled) and recompile."
            )
        if vc < n_sources:
            raise CompileError(
                f"compile for {key} produced {vc} vector entries for {n_sources} "
                f"sources — embeddings are not live for all compiled content; "
                f"refusing to run questions against a raw/BM25-dominated project."
            )
        marker.write_text(str(n_sources), encoding="utf-8")
        return CompileStats(skipped=False, seconds=time.monotonic() - start,
                            vector_count=vc, source_count=n_sources)

    # -- search ----------------------------------------------------------------

    @staticmethod
    def _degraded(stderr: str, rows: list[dict]) -> bool:
        if any(marker in stderr for marker in EMBED_WARNINGS):
            return True
        if rows and not any((r.get("VectorRank") or 0) > 0 for r in rows):
            return True  # silent config-load degrade: vector branch contributed nothing
        return False

    @staticmethod
    def _rate_limited(stderr: str) -> bool:
        """The embedder degraded because the provider is throttling us — a
        transient condition worth waiting out, unlike a config-load degrade."""
        low = stderr.lower()
        degraded = any(marker in stderr for marker in EMBED_WARNINGS)
        return degraded and any(m in low for m in RATE_LIMIT_MARKERS)

    def search(self, key: str, query: str, limit: int = 10,
               timeout_s: int = 300) -> SearchResponse:
        """Search with rate-limit-aware retries.

        The query embedding happens inside the sage-wiki subprocess, so a
        throttled provider surfaces here as an exit-0 BM25-only degrade rather
        than an exception. Those attempts go through the shared gate (pausing
        every worker) and get more tries than a permanent degrade, which no
        amount of retrying will fix.
        """
        last_reason = ""
        attempt = 0
        max_attempts = 1 + SEARCH_RETRIES
        while attempt < max_attempts:
            attempt += 1
            self.gate.wait()  # honor a cooldown opened anywhere in the run
            start = time.monotonic()
            proc = self._run(key, "search", query, "--format", "json",
                             "--limit", str(limit), timeout_s=timeout_s)
            latency_ms = (time.monotonic() - start) * 1000
            if proc.returncode != 0:
                last_reason = f"exit {proc.returncode}: {proc.stderr[-200:]}"
                continue
            try:
                envelope = json.loads(proc.stdout)
            except json.JSONDecodeError as exc:
                last_reason = f"undecodable stdout: {exc}"
                continue
            rows = envelope.get("data") or []  # zero hits arrive as data: null
            if self._degraded(proc.stderr, rows):
                if self._rate_limited(proc.stderr):
                    max_attempts = max(max_attempts, 1 + SEARCH_RATE_RETRIES)
                    last_reason = ("embedder rate-limited by the provider "
                                   "(search degraded to BM25-only)")
                    self.gate.report_limit()  # may raise QuotaExhausted → abort run
                else:
                    last_reason = "BM25-only degrade detected"
                continue
            results = [
                {"memory": r.get("Content", ""),
                 # Post-upgrade the pipeline ranks by FinalScore; RRFScore is
                 # the pre-upgrade field and stays as the fallback so one
                 # harness scores both binaries by whatever they ranked on.
                 "score": r["FinalScore"] if "FinalScore" in r else r.get("RRFScore", 0.0),
                 "id": r.get("ID", "")}
                for r in rows
            ]
            self.gate.report_success()
            return SearchResponse(results=results, latency_ms=latency_ms, raw=rows)
        raise DegradedSearchError(
            f"search stayed degraded/failing after {attempt} attempts "
            f"for {key}: {last_reason}"
        )

    def binary_version(self) -> str:
        try:
            proc = subprocess.run([self.binary, "version"], capture_output=True,
                                  text=True, timeout=30)
            return proc.stdout.strip()
        except (OSError, subprocess.TimeoutExpired):
            return "unknown"
