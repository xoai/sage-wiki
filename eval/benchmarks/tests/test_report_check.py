"""T13 — report_check: quoted REPORT numbers must match the results JSONs."""

import json
import subprocess
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "report_check.py"

RESULTS = {
    "metadata": {"benchmark": "locomo", "project_name": "full", "total_questions": 3},
    "metrics": {
        "overall": {"total": 3, "correct": 2, "accuracy": 66.66666, "avg_score": 66.7,
                    "infra_errors": 1},
        "by_group": {"temporal": {"total": 1, "correct": 1, "accuracy": 100.0,
                                  "avg_score": 100.0}},
    },
    "latency": {"count": 3, "p50_ms": 120.0, "p95_ms": 900.0, "avg_ms": 300.0},
}

REPORT_OK = """# Report
<!-- check:locomo_full metrics.overall.accuracy = 66.7 -->
LOCOMO overall accuracy: **66.7%** (2/3 scored questions, 1 infra error).
<!-- check:locomo_full metrics.by_group.temporal.accuracy = 100.0 -->
Temporal: 100.0%.
"""

REPORT_BAD = """# Report
<!-- check:locomo_full metrics.overall.accuracy = 80.0 -->
LOCOMO overall accuracy: **80.0%**.
"""


def run_check(tmp_path, report_text):
    results = tmp_path / "results"
    results.mkdir()
    (results / "locomo_full.json").write_text(json.dumps(RESULTS))
    report = tmp_path / "REPORT.md"
    report.write_text(report_text)
    return subprocess.run([sys.executable, str(SCRIPT), "--report", str(report),
                          "--results-dir", str(results)],
                         capture_output=True, text=True)


class TestReportCheck:
    def test_matching_numbers_exit_zero(self, tmp_path):
        proc = run_check(tmp_path, REPORT_OK)
        assert proc.returncode == 0, proc.stdout + proc.stderr
        assert "2 checks" in proc.stdout

    def test_mismatching_number_exits_nonzero(self, tmp_path):
        proc = run_check(tmp_path, REPORT_BAD)
        assert proc.returncode != 0
        assert "80.0" in proc.stdout and "66.7" in proc.stdout

    def test_unknown_results_file_fails(self, tmp_path):
        proc = run_check(tmp_path, "<!-- check:nope_x metrics.overall.total = 1 -->\n")
        assert proc.returncode != 0

    def test_report_without_checks_fails(self, tmp_path):
        proc = run_check(tmp_path, "# empty report\n")
        assert proc.returncode != 0  # a report with zero checks is unverified
