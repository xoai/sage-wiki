package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const validSHA = "58166c18e49aceed83651388e1108bd381b683aa"

func makeFixture(name string) ProofFixture {
	return ProofFixture{
		SHA:              validSHA,
		MainReachable:    true,
		ExpectedAppID:    15368,
		ExpectedContext:  "CI required",
		ExpectedWorkflow: "CI",
	}
}

func validRun(sha string) RunFixture {
	return RunFixture{
		ID:         1001,
		Event:      "push",
		HeadSHA:    sha,
		HeadBranch: "main",
		Name:       "CI",
		Path:       ".github/workflows/ci.yml",
		Status:     "completed",
		Conclusion: "success",
		CreatedAt:  "2026-08-13T00:53:16Z",
		Attempts: []AttemptFixture{
			{RunAttempt: 1, Status: "completed", Conclusion: "success"},
		},
		LatestAttemptJobs: []JobFixture{
			{Name: "CI required", Conclusion: "success", AppID: 15368},
		},
	}
}

func TestProof(t *testing.T) {
	tests := []struct {
		name       string
		build      func() ProofFixture
		wantValid  bool
		wantReason string
	}{
		{
			name:      "valid proof",
			build:     func() ProofFixture { f := makeFixture("valid"); f.Runs = []RunFixture{validRun(validSHA)}; return f },
			wantValid: true,
		},
		{
			name:       "absent proof — no matching runs",
			build:      func() ProofFixture { f := makeFixture("absent"); return f },
			wantValid:  false,
			wantReason: "no matching",
		},
		{
			name: "pending attempt blocks proof",
			build: func() ProofFixture {
				f := makeFixture("pending")
				r := validRun(validSHA)
				r.Status = "in_progress"
				r.Attempts[0].Status = "in_progress"
				r.Attempts[0].Conclusion = ""
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "non-success",
		},
		{
			name: "initial red then rerun green blocks proof",
			build: func() ProofFixture {
				f := makeFixture("rerun")
				r := validRun(validSHA)
				r.Attempts = []AttemptFixture{
					{RunAttempt: 1, Status: "completed", Conclusion: "failure"},
					{RunAttempt: 2, Status: "completed", Conclusion: "success"},
				}
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "non-success",
		},
		{
			name: "cancelled attempt blocks proof",
			build: func() ProofFixture {
				f := makeFixture("cancelled")
				r := validRun(validSHA)
				r.Attempts[0].Conclusion = "cancelled"
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "non-success",
		},
		{
			name: "wrong SHA blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-sha")
				r := validRun("wrong1234567890")
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "no matching",
		},
		{
			name: "wrong branch blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-branch")
				r := validRun(validSHA)
				r.HeadBranch = "develop"
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "no matching",
		},
		{
			name: "wrong event blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-event")
				r := validRun(validSHA)
				r.Event = "pull_request"
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "no matching",
		},
		{
			name: "wrong workflow blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-workflow")
				r := validRun(validSHA)
				r.Name = "Other"
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "no matching",
		},
		{
			name: "wrong app ID blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-app")
				r := validRun(validSHA)
				r.LatestAttemptJobs[0].AppID = 99999
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "CI required",
		},
		{
			name: "wrong context name blocks proof",
			build: func() ProofFixture {
				f := makeFixture("wrong-context")
				r := validRun(validSHA)
				r.LatestAttemptJobs[0].Name = "build"
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "CI required",
		},
		{
			name: "duplicate matching run IDs blocks proof",
			build: func() ProofFixture {
				f := makeFixture("duplicate")
				r1 := validRun(validSHA)
				r2 := validRun(validSHA)
				r2.ID = 1002
				f.Runs = []RunFixture{r1, r2}
				return f
			},
			wantValid:  false,
			wantReason: "ambiguous",
		},
		{
			name: "unreachable SHA blocks proof",
			build: func() ProofFixture {
				f := makeFixture("unreachable")
				f.MainReachable = false
				f.Runs = []RunFixture{validRun(validSHA)}
				return f
			},
			wantValid:  false,
			wantReason: "unreachable",
		},
		{
			name: "missing CI required job blocks proof",
			build: func() ProofFixture {
				f := makeFixture("missing-job")
				r := validRun(validSHA)
				r.LatestAttemptJobs = []JobFixture{}
				f.Runs = []RunFixture{r}
				return f
			},
			wantValid:  false,
			wantReason: "CI required",
		},
		{
			name: "valid emergency waiver for red SHA",
			build: func() ProofFixture {
				f := makeFixture("waiver")
				r := validRun(validSHA)
				r.Attempts[0].Conclusion = "failure"
				f.Runs = []RunFixture{r}
				f.Waiver = &WaiverFixture{
					Approver:         "xoai",
					Reason:           "Runner capacity outage caused red; confirmed not a code regression.",
					IncidentLink:     "https://github.com/xoai/sage-wiki/issues/200",
					SHA:              validSHA,
					ExpiresAt:        "2026-08-20T00:00:00Z",
					OriginalEvidence: "CI required failed due to runner timeout, not code defect.",
				}
				return f
			},
			wantValid: true,
		},
		{
			name: "expired waiver does not grant authority",
			build: func() ProofFixture {
				f := makeFixture("waiver-expired")
				r := validRun(validSHA)
				r.Attempts[0].Conclusion = "failure"
				f.Runs = []RunFixture{r}
				f.Waiver = &WaiverFixture{
					Approver:         "xoai",
					Reason:           "expired",
					IncidentLink:     "https://example.com",
					SHA:              validSHA,
					ExpiresAt:        "2020-01-01T00:00:00Z",
					OriginalEvidence: "old",
				}
				return f
			},
			wantValid:  false,
			wantReason: "expired",
		},
		{
			name: "waiver missing required fields does not grant authority",
			build: func() ProofFixture {
				f := makeFixture("waiver-incomplete")
				r := validRun(validSHA)
				r.Attempts[0].Conclusion = "failure"
				f.Runs = []RunFixture{r}
				f.Waiver = &WaiverFixture{
					Approver:  "xoai",
					ExpiresAt: "2026-08-20T00:00:00Z",
				}
				return f
			},
			wantValid:  false,
			wantReason: "incomplete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := tt.build()
			result := evaluateProof(fixture)
			if result.Valid != tt.wantValid {
				t.Fatalf("valid = %v (%s), want %v", result.Valid, result.Reason, tt.wantValid)
			}
			if !tt.wantValid && tt.wantReason != "" {
				if !contains(result.Reason, tt.wantReason) {
					t.Fatalf("reason = %q, want substring %q", result.Reason, tt.wantReason)
				}
			}
		})
	}
}

func TestRunFixtures(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "proof.json")

	// Write the valid fixture to testdata for the CLI.
	validFixture := makeFixture("valid")
	validFixture.Runs = []RunFixture{validRun(validSHA)}
	data, _ := json.Marshal(validFixture)
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytesBuf
	code := run([]string{"--fixtures", dir, "--sha", validSHA, "--output", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}

	proofData, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var pr ProofResult
	if err := json.Unmarshal(proofData, &pr); err != nil {
		t.Fatal(err)
	}
	if !pr.Valid {
		t.Fatalf("proof result = %+v, want valid", pr)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSub(s, sub))
}

func findSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type bytesBuf struct{ b []byte }

func (b *bytesBuf) Write(p []byte) (int, error) { b.b = append(b.b, p...); return len(p), nil }
func (b *bytesBuf) String() string              { return string(b.b) }
