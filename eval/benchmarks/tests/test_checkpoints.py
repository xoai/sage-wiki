"""T4 — checkpoint store: atomic writes, resume scan, run metadata, heartbeat."""

import json
import logging

from benchmarks.common.checkpoints import CheckpointStore, heartbeat, write_run_metadata


class TestCheckpointStore:
    def test_save_and_reload(self, tmp_path):
        store = CheckpointStore(tmp_path / "out")
        store.save("conv0_q1", {"question_id": "conv0_q1", "judgment": "CORRECT"})
        assert store.done_ids() == {"conv0_q1"}
        assert store.load("conv0_q1")["judgment"] == "CORRECT"

    def test_save_is_atomic_no_tmp_left_behind(self, tmp_path):
        store = CheckpointStore(tmp_path / "out")
        store.save("q1", {"a": 1})
        leftovers = [p for p in (tmp_path / "out").iterdir() if p.suffix != ".json"]
        assert leftovers == []

    def test_resume_skips_done(self, tmp_path):
        store = CheckpointStore(tmp_path / "out")
        store.save("q1", {"x": 1})
        store2 = CheckpointStore(tmp_path / "out")
        assert "q1" in store2.done_ids()
        assert "q2" not in store2.done_ids()

    def test_corrupt_file_not_counted_done(self, tmp_path):
        out = tmp_path / "out"
        out.mkdir()
        (out / "bad.json").write_text("{not json")
        store = CheckpointStore(out)
        assert store.done_ids() == set()

    def test_load_all(self, tmp_path):
        store = CheckpointStore(tmp_path / "out")
        store.save("q1", {"question_id": "q1"})
        store.save("q2", {"question_id": "q2"})
        ids = sorted(r["question_id"] for r in store.load_all())
        assert ids == ["q1", "q2"]


class TestRunMetadata:
    def test_written_with_required_fields(self, tmp_path):
        path = write_run_metadata(
            tmp_path, benchmark="locomo", project_name="t",
            binary_version="sage-wiki dev (commit abc)",
            models={"answerer": "m", "judge": "m", "compile": "m"},
            scope={"conversations": [0]}, status="running",
        )
        data = json.loads(path.read_text())
        assert data["benchmark"] == "locomo"
        assert data["binary_version"].startswith("sage-wiki")
        assert data["status"] == "running"
        # update in place (e.g. failure path still records)
        write_run_metadata(tmp_path, benchmark="locomo", project_name="t",
                           binary_version="sage-wiki dev (commit abc)",
                           models={}, scope={}, status="failed", error="boom")
        data = json.loads(path.read_text())
        assert data["status"] == "failed" and data["error"] == "boom"


class TestHeartbeat:
    def test_logs_every_n(self, caplog):
        caplog.set_level(logging.INFO)
        log = logging.getLogger("hb-test")
        for i in range(1, 51):
            heartbeat(log, done=i, total=100, every=25)
        msgs = [r.message for r in caplog.records]
        assert len(msgs) == 2
        assert "25/100" in msgs[0] and "50/100" in msgs[1]
