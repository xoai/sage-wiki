package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeRun(workflow, sha, event, conclusion string, created time.Time, jobSeconds float64) RunObservation {
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
		Jobs: []JobObservation{{
			Name:           workflow + "-job",
			Status:         "completed",
			Conclusion:     conclusion,
			StartedAt:      created.UTC().Format(time.RFC3339),
			CompletedAt:    created.Add(time.Duration(jobSeconds) * time.Second).UTC().Format(time.RFC3339),
			RuntimeSeconds: jobSeconds,
			RunnerOS:       "ubuntu-latest",
		}},
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
		name     string
		runs     []RunObservation
		want     string
		wantCode int // 0 qualified, 1 insufficient/divergent
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
				// Make the 10th pair divergent: current success, candidate failure
				runs[19].Conclusion = "failure" // CI Shadow run for pair 9
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (insufficient); stderr=%s", code, stderr.String())
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
