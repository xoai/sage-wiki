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

EMBED_WARNING = "warning: embed failed, using BM25-only"
SEARCH_RETRIES = 2  # retries after the initial attempt
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
                 model: str = "gpt-4o-mini", embed_model: str = "text-embedding-3-small"):
        self.binary = binary or default_binary()
        self.root = Path(root)
        self.model = model
        self.embed_model = embed_model

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

    def vector_count(self, key: str) -> int:
        proc = self._run(key, "status", "--format", "json", timeout_s=120)
        try:
            data = json.loads(proc.stdout).get("data") or {}
        except json.JSONDecodeError:
            return 0
        return int(data.get("vector_count") or 0)

    def compile(self, key: str, timeout_s: int = 7200) -> CompileStats:
        proj = self.project_dir(key)
        marker = proj / ".compiled"
        n_sources = self._source_count(key)
        if marker.is_file() and marker.read_text().strip() == str(n_sources):
            return CompileStats(skipped=True, seconds=0.0,
                                vector_count=self.vector_count(key),
                                source_count=n_sources)

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

        vc = self.vector_count(key)
        if vc <= 0:
            raise CompileError(
                f"compile for {key} produced no vector entries — embeddings are not "
                f"live; refusing to run questions against a BM25-only project."
            )
        marker.write_text(str(n_sources), encoding="utf-8")
        return CompileStats(skipped=False, seconds=time.monotonic() - start,
                            vector_count=vc, source_count=n_sources)

    # -- search ----------------------------------------------------------------

    @staticmethod
    def _degraded(stderr: str, rows: list[dict]) -> bool:
        if EMBED_WARNING in stderr:
            return True
        if rows and not any((r.get("VectorRank") or 0) > 0 for r in rows):
            return True  # silent config-load degrade: vector branch contributed nothing
        return False

    def search(self, key: str, query: str, limit: int = 10,
               timeout_s: int = 300) -> SearchResponse:
        last_reason = ""
        for _ in range(1 + SEARCH_RETRIES):
            start = time.monotonic()
            proc = self._run(key, "search", query, "--format", "json",
                             "--limit", str(limit), timeout_s=timeout_s)
            latency_ms = (time.monotonic() - start) * 1000
            if proc.returncode != 0:
                last_reason = f"exit {proc.returncode}: {proc.stderr[-200:]}"
                continue
            envelope = json.loads(proc.stdout)
            rows = envelope.get("data") or []  # zero hits arrive as data: null
            if self._degraded(proc.stderr, rows):
                last_reason = "BM25-only degrade detected"
                continue
            results = [
                {"memory": r.get("Content", ""), "score": r.get("RRFScore", 0.0),
                 "id": r.get("ID", "")}
                for r in rows
            ]
            return SearchResponse(results=results, latency_ms=latency_ms, raw=rows)
        raise DegradedSearchError(
            f"search stayed degraded/failing after {1 + SEARCH_RETRIES} attempts "
            f"for {key}: {last_reason}"
        )

    def binary_version(self) -> str:
        try:
            proc = subprocess.run([self.binary, "version"], capture_output=True,
                                  text=True, timeout=30)
            return proc.stdout.strip()
        except (OSError, subprocess.TimeoutExpired):
            return "unknown"
