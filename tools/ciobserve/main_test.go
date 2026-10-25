package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeRun builds a run whose jobs mirror the real workflows' responsibility
// split: the Go test path ("Test (race)") is SHARED by CI and CI Shadow;
// vuln-scan/frontend/fuzz exist only in CI (coverage, not shared verdicts).
func makeRun(workflow, sha, event, conclusion string, created time.Time, jobSeconds float64) RunObservation {
	shared := JobObservation{
		Name:           "Test (race)",
		Status:         "completed",
		Conclusion:     conclusion,
		StartedAt:      created.UTC().Format(time.RFC3339),
		CompletedAt:    created.Add(time.Duration(jobSeconds) * time.Second).UTC().Format(time.RFC3339),
		RuntimeSeconds: jobSeconds,
		RunnerOS:       "ubuntu-latest",
	}
	jobs := []JobObservation{shared}
	if workflow == "CI" {
		jobs = append(jobs,
			JobObservation{Name: "Vuln scan (advisory)", Status: "completed", Conclusion: "success", RunnerOS: "ubuntu-latest"},
			JobObservation{Name: "Frontend (typecheck + build)", Status: "completed", Conclusion: "success", RunnerOS: "ubuntu-latest"},
		)
	}
	return RunObservation{
		Workflow:    workflow,
		RunID:       created.Unix(),
		Event:       event,
		Attempt:     1,
		HeadSHA:     sha,
		HeadBranch:  "main",
		CheckoutSHA: sha,
		CreatedAt:   created.UTC().Format(time.RFC3339),
		Status:      "completed",
		Conclusion:  conclusion,
		Jobs:        jobs,
	}
}

func makePair(sha, event string, day int, currentOK, candidateOK bool, secs float64) []RunObservation {
	cc := "success"
	if !currentOK {
		cc = "failure"
	}
	ct := "success"
	if !candidateOK {
		ct = "failure"
	}
	t := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC).AddDate(0, 0, day)
	return []RunObservation{
		makeRun("CI", sha, event, cc, t, secs),
		makeRun("CI Shadow", sha, event, ct, t.Add(time.Minute), secs),
	}
}

func TestQualification(t *testing.T) {
	opts := QualificationOptions{MinimumRuns: 20, MinimumDays: 7, MaxP95Seconds: 480}

	// Helper: build N matched pairs spanning `days` days.
	buildPairs := func(n, days int, allOK bool, p95Secs float64) []RunObservation {
		var runs []RunObservation
		for i := 0; i < n; i++ {
			day := i * days / max(n-1, 1)
			sha := "sha" + string(rune('a'+i))
			runs = append(runs, makePair(sha, "pull_request", day, allOK, allOK, p95Secs*0.9)...)
		}
		return runs
	}

	tests := []struct {
		name string
		runs []RunObservation
		want string
		// wantCode: 0 qualified/insufficient (insufficient = healthy
		// accumulation — a scheduled observer must NOT alert on it), 1
		// divergent (same-tree mismatch: the one alert-worthy state).
	}{
		{
			name: "qualified",
			runs: buildPairs(20, 7, true, 300),
			want: "qualified",
		},
		{
			name: "insufficient run count",
			runs: buildPairs(5, 7, true, 300),
			want: "insufficient",
		},
		{
			name: "insufficient date span",
			runs: buildPairs(20, 3, true, 300),
			want: "insufficient",
		},
		{
			name: "divergent identical tree",
			runs: func() []RunObservation {
				runs := buildPairs(20, 7, true, 300)
				// Make the last pair divergent: candidate's shared job fails
				// (a failed GitHub run always has a failed job — conclusion
				// alone is not observable evidence at job scope).
				runs[19].Conclusion = "failure" // CI Shadow run for pair 9
				runs[19].Jobs[0].Conclusion = "failure"
				return runs
			}(),
			want: "divergent",
		},
		{
			name: "vacuous candidate jobs",
			runs: func() []RunObservation {
				runs := buildPairs(20, 7, true, 300)
				// Empty the jobs of one candidate run
				runs[19].Jobs = nil
				return runs
			}(),
			want: "insufficient",
		},
		{
			name: "p95 over bound",
			runs: buildPairs(20, 7, true, 600),
			want: "insufficient",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := evaluateQualification(tt.runs, opts)
			if rec.Status != tt.want {
				t.Fatalf("status = %q (%s), want %q", rec.Status, rec.Reason, tt.want)
			}
		})
	}
}

// TestCoverageDifferenceIsNotDivergence pins the disposition lesson from the
// 2026-10-24 qualification window: CI-only coverage jobs (vuln scan, frontend
// dist verify, fuzz) failing on a main-side push while the shared Go test path
// agrees green is a COVERAGE difference, not a candidate-quality divergence.
// The whole-run conclusion mismatch must not flag it.
func TestCoverageDifferenceIsNotDivergence(t *testing.T) {
	opts := QualificationOptions{MinimumRuns: 20, MinimumDays: 7, MaxP95Seconds: 1100}
	runs := func() []RunObservation {
		var out []RunObservation
		for i := 0; i < 20; i++ {
			day := i
			sha := "cov" + string(rune('a'+i))
			out = append(out, makePair(sha, "push", day, true, true, 300)...)
		}
		// Two CI runs fail on CI-ONLY jobs; shared "Test (race)" stays green
		// on both sides — the exact live shape of 3669810 and d8ca516.
		for _, idx := range []int{1, 37} { // run slices: CI at even, Shadow at odd index
			if idx%2 == 1 {
				idx--
			}
			out[idx].Conclusion = "failure"
			for j := range out[idx].Jobs {
				if out[idx].Jobs[j].Name == "Vuln scan (advisory)" || out[idx].Jobs[j].Name == "Frontend (typecheck + build)" {
					out[idx].Jobs[j].Conclusion = "failure"
				}
			}
		}
		return out
	}()
	rec := evaluateQualification(runs, opts)
	if rec.Status != "qualified" {
		t.Fatalf("status = %q (%s), want qualified — CI-only coverage failures with green shared jobs are not divergence", rec.Status, rec.Reason)
	}
	if rec.P50Seconds <= 0 || rec.P95Seconds <= 0 {
		t.Fatalf("timing fields not populated on qualified path: p50=%v p95=%v", rec.P50Seconds, rec.P95Seconds)
	}
}

// TestSharedJobMismatchIsDivergence: a conclusion mismatch on a job BOTH
// workflows run is the alert-worthy divergence — in either direction.
func TestSharedJobMismatchIsDivergence(t *testing.T) {
	opts := QualificationOptions{MinimumRuns: 20, MinimumDays: 7, MaxP95Seconds: 1100}

	// Candidate-side shared failure, current green (the classic promotion blocker).
	runs := func() []RunObservation {
		out := func() []RunObservation {
			var r []RunObservation
			for i := 0; i < 20; i++ {
				r = append(r, makePair("sd"+string(rune('a'+i)), "push", i, true, true, 300)...)
			}
			return r
		}()
		out[39].Conclusion = "failure" // shadow side of last pair
		for j := range out[39].Jobs {
			out[39].Jobs[j].Conclusion = "failure"
		}
		return out
	}()
	rec := evaluateQualification(runs, opts)
	if rec.Status != "divergent" {
		t.Fatalf("candidate shared-job failure: status = %q (%s), want divergent", rec.Status, rec.Reason)
	}
	if len(rec.Divergences) != 1 || rec.Divergences[0].Explanation == "" {
		t.Fatalf("divergence must carry a shared-job explanation, got %+v", rec.Divergences)
	}

	// Current-side shared failure, candidate green: symmetric mismatch.
	runs2 := func() []RunObservation {
		r := func() []RunObservation {
			var out []RunObservation
			for i := 0; i < 20; i++ {
				out = append(out, makePair("sd2"+string(rune('a'+i)), "push", i, true, true, 300)...)
			}
			return out
		}()
		r[38].Conclusion = "failure" // CI side of last pair
		for j := range r[38].Jobs {
			r[38].Jobs[j].Conclusion = "failure"
		}
		return r
	}()
	rec2 := evaluateQualification(runs2, opts)
	if rec2.Status != "divergent" {
		t.Fatalf("current shared-job failure: status = %q (%s), want divergent", rec2.Status, rec2.Reason)
	}

	// Candidate-ONLY job failure (no current counterpart) — the shadow's own
	// regression — must alert too.
	runs3 := func() []RunObservation {
		var out []RunObservation
		for i := 0; i < 20; i++ {
			out = append(out, makePair("sd3"+string(rune('a'+i)), "push", i, true, true, 300)...)
		}
		out[39].Jobs = append(out[39].Jobs, JobObservation{
			Name: "Shadow responsibility validation", Status: "completed", Conclusion: "failure",
		})
		return out
	}()
	rec3 := evaluateQualification(runs3, opts)
	if rec3.Status != "divergent" {
		t.Fatalf("candidate-only job failure: status = %q (%s), want divergent", rec3.Status, rec3.Reason)
	}
}

// TestNoSharedJobsIsVacuous: a candidate sharing zero job names with the
// current run qualifies nothing — the pair is insufficient, not silent evidence.
func TestNoSharedJobsIsVacuous(t *testing.T) {
	opts := QualificationOptions{MinimumRuns: 20, MinimumDays: 7, MaxP95Seconds: 1100}
	runs := func() []RunObservation {
		var r []RunObservation
		for i := 0; i < 20; i++ {
			r = append(r, makePair("vac"+string(rune('a'+i)), "push", i, true, true, 300)...)
		}
		return r
	}()
	// Rename every shadow job so nothing overlaps.
	for i := range runs {
		if runs[i].Workflow == "CI Shadow" {
			for j := range runs[i].Jobs {
				runs[i].Jobs[j].Name = "shadow-only " + runs[i].Jobs[j].Name
			}
		}
	}
	rec := evaluateQualification(runs, opts)
	if rec.Status != "insufficient" {
		t.Fatalf("status = %q (%s), want insufficient — no shared jobs means vacuous comparison", rec.Status, rec.Reason)
	}
}

// TestRuntimeDerivedFromTimestamps: live fixtures carry started_at/completed_at
// but no runtime_seconds; occupancy must still populate.
func TestRuntimeDerivedFromTimestamps(t *testing.T) {
	opts := QualificationOptions{MinimumRuns: 20, MinimumDays: 7, MaxP95Seconds: 1100}
	var runs []RunObservation
	for i := 0; i < 20; i++ {
		runs = append(runs, makePair("rt"+string(rune('a'+i)), "push", i, true, true, 300)...)
	}
	for i := range runs {
		for j := range runs[i].Jobs {
			runs[i].Jobs[j].RuntimeSeconds = 0
		}
	}
	rec := evaluateQualification(runs, opts)
	if rec.Status != "qualified" {
		t.Fatalf("status = %q (%s), want qualified", rec.Status, rec.Reason)
	}
	if rec.RunnerOccupancy <= 0 {
		t.Fatalf("runner occupancy = %v, want > 0 (derived from job timestamps)", rec.RunnerOccupancy)
	}
}

// TestMetricsBaseline covers the Task 22 baseline evaluator: five comparable
// successful same-DAG runs, wall time measured from job execution spans
// (NEVER queue-inclusive), per-run occupancy and slowest-job attribution,
// and the four rejections (too few runs, mixed DAG revisions, missing jobs,
// unsuccessful runs).
func TestMetricsBaseline(t *testing.T) {
	opts := MetricsOptions{MinimumRuns: 5}

	buildRuns := func(n int) []RunObservation {
		var runs []RunObservation
		for i := 0; i < n; i++ {
			created := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
			r := makeRun("CI", "sha"+string(rune('a'+i)), "push", "success", created, 300)
			// Queue delay: jobs start 4 minutes after run creation. A
			// queue-inclusive wall implementation would report 640s; the
			// execution span is 300s.
			r.DagRevision = "dag-854f29"
			for j := range r.Jobs {
				r.Jobs[j].StartedAt = created.Add(4 * time.Minute).UTC().Format(time.RFC3339)
				r.Jobs[j].CompletedAt = created.Add(4*time.Minute + 300*time.Second).UTC().Format(time.RFC3339)
				r.Jobs[j].QueueSeconds = 240
			}
			runs = append(runs, r)
		}
		return runs
	}

	t.Run("five comparable runs produce a baseline", func(t *testing.T) {
		rec := evaluateMetrics(buildRuns(5), opts)
		if rec.Status != "ok" {
			t.Fatalf("status = %q (%s), want ok", rec.Status, rec.Reason)
		}
		for _, rm := range rec.Runs {
			if rm.WallSeconds != 300 {
				t.Fatalf("wall = %v, want 300 (execution span, not queue-inclusive 540)", rm.WallSeconds)
			}
		}
		if rec.SlowestJob == "" || rec.SlowestJobSeconds != 300 {
			t.Fatalf("slowest job attribution wrong: %q %v", rec.SlowestJob, rec.SlowestJobSeconds)
		}
		if rec.MedianQueueSeconds != 240 {
			t.Fatalf("median queue = %v, want 240", rec.MedianQueueSeconds)
		}
	})

	rejections := []struct {
		name   string
		mutate func([]RunObservation) []RunObservation
		want   string
	}{
		{"fewer than five runs", func(r []RunObservation) []RunObservation { return r[:4] }, "insufficient"},
		{"mixed DAG revisions", func(r []RunObservation) []RunObservation {
			r[2].DagRevision = "dag-older"
			return r
		}, "mixed-dag"},
		{"missing job timing", func(r []RunObservation) []RunObservation {
			r[3].Jobs = nil
			return r
		}, "missing-jobs"},
		{"unsuccessful run", func(r []RunObservation) []RunObservation {
			r[4].Conclusion = "failure"
			return r
		}, "not-success"},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			rec := evaluateMetrics(tt.mutate(buildRuns(5)), opts)
			if rec.Status != tt.want {
				t.Fatalf("status = %q (%s), want %q", rec.Status, rec.Reason, tt.want)
			}
		})
	}
}

func TestRunFixtures(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "qualification.json")

	// Write a small fixture set: 2 matched pairs (insufficient).
	fixture := makePair("sha-a", "pull_request", 0, true, true, 200)
	fixture = append(fixture, makePair("sha-b", "pull_request", 1, true, true, 220)...)
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytesLike
	code := run([]string{"--fixtures", dir, "--output", out, "--minimum-runs", "20", "--minimum-days", "7"}, &stdout, &stderr)
	// Exit contract (issue: observer red daily during accumulation): 2
	// matched pairs < minimum 20 → insufficient → exit 0, NOT 1 — the
	// window filling is the observer's normal intermediate state. Only
	// divergence (same-tree conclusion mismatch) exits 1.
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (insufficient = healthy accumulation); stderr=%s", code, stderr.String())
	}

	rec, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var qr QualificationRecord
	if err := json.Unmarshal(rec, &qr); err != nil {
		t.Fatal(err)
	}
	if qr.Status != "insufficient" {
		t.Fatalf("status = %q, want insufficient", qr.Status)
	}
	if qr.SampleCount != 2 {
		t.Fatalf("sample_count = %d, want 2", qr.SampleCount)
	}
}

func TestP50P95(t *testing.T) {
	times := []float64{100, 200, 300, 400, 500}
	p50 := percentile(times, 0.50)
	p95 := percentile(times, 0.95)
	if p50 != 300 {
		t.Fatalf("p50 = %v, want 300", p50)
	}
	if p95 != 500 {
		t.Fatalf("p95 = %v, want 500", p95)
	}
}

type bytesLike struct {
	b []byte
}

func (b *bytesLike) Write(p []byte) (int, error) { b.b = append(b.b, p...); return len(p), nil }
func (b *bytesLike) String() string              { return string(b.b) }
