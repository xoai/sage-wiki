"""Per-question checkpoint files (the resume unit), run metadata, heartbeat."""

from __future__ import annotations

import json
import logging
import os
from pathlib import Path


class CheckpointStore:
    """One JSON file per question under `out_dir`; tmp+rename keeps writes atomic,
    so a killed run loses at most the question in flight."""

    def __init__(self, out_dir: Path | str):
        self.out_dir = Path(out_dir)
        self.out_dir.mkdir(parents=True, exist_ok=True)

    def _path(self, qid: str) -> Path:
        return self.out_dir / f"{qid}.json"

    def save(self, qid: str, record: dict) -> Path:
        path = self._path(qid)
        tmp = path.with_name(path.name + ".tmp")
        tmp.write_text(json.dumps(record, ensure_ascii=False, indent=1), encoding="utf-8")
        os.replace(tmp, path)
        return path

    def load(self, qid: str) -> dict:
        return json.loads(self._path(qid).read_text(encoding="utf-8"))

    def done_ids(self) -> set[str]:
        done = set()
        for p in self.out_dir.glob("*.json"):
            if p.name.startswith("_"):
                continue
            try:
                json.loads(p.read_text(encoding="utf-8"))
            except (json.JSONDecodeError, OSError):
                continue
            done.add(p.stem)
        return done

    def load_all(self) -> list[dict]:
        rows = []
        for p in sorted(self.out_dir.glob("*.json")):
            if p.name.startswith("_"):
                continue
            try:
                rows.append(json.loads(p.read_text(encoding="utf-8")))
            except (json.JSONDecodeError, OSError):
                continue
        return rows


def write_run_metadata(out_dir: Path | str, **fields) -> Path:
    """Create or update _run_metadata.json — also called on failure paths so a
    crashed run still records binary version, models, and the error."""
    path = Path(out_dir) / "_run_metadata.json"
    data = {}
    if path.is_file():
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            data = {}
    data.update({k: v for k, v in fields.items() if v is not None})
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
    os.replace(tmp, path)
    return path


def heartbeat(log: logging.Logger, done: int, total: int, every: int = 25) -> None:
    if every > 0 and done % every == 0:
        log.info("progress: %d/%d questions", done, total)
