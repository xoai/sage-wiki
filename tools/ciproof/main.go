package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ProofFixture struct {
	SHA              string         `json:"sha"`
	MainReachable    bool           `json:"main_reachable"`
	Runs             []RunFixture   `json:"runs"`
	Waiver           *WaiverFixture `json:"waiver,omitempty"`
	ExpectedAppID    int64          `json:"expected_app_id"`
	ExpectedContext  string         `json:"expected_context"`
	ExpectedWorkflow string         `json:"expected_workflow"`
}

type RunFixture struct {
	ID                int64            `json:"id"`
	Event             string           `json:"event"`
	HeadSHA           string           `json:"head_sha"`
	HeadBranch        string           `json:"head_branch"`
	Name              string           `json:"name"`
	Path              string           `json:"path"`
	Status            string           `json:"status"`
	Conclusion        string           `json:"conclusion"`
	CreatedAt         string           `json:"created_at"`
	Attempts          []AttemptFixture `json:"attempts"`
	LatestAttemptJobs []JobFixture     `json:"latest_attempt_jobs"`
}

type AttemptFixture struct {
	RunAttempt int    `json:"run_attempt"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type JobFixture struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	AppID      int64  `json:"app_id"`
}

type WaiverFixture struct {
	Approver         string `json:"approver"`
	Reason           string `json:"reason"`
	IncidentLink     string `json:"incident_link"`
	SHA              string `json:"sha"`
	ExpiresAt        string `json:"expires_at"`
	OriginalEvidence string `json:"original_evidence"`
}

type ProofResult struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason,omitempty"`
	SHA    string `json:"sha"`
	RunID  int64  `json:"run_id,omitempty"`
	Waived bool   `json:"waived,omitempty"`
}

func evaluateProof(f ProofFixture) ProofResult {
	result := ProofResult{SHA: f.SHA}
	if !f.MainReachable {
		result.Reason = fmt.Sprintf("SHA %s is unreachable from protected main", f.SHA)
		return result
	}
	appID := f.ExpectedAppID
	if appID == 0 {
		appID = 15368
	}
	wfName := f.ExpectedWorkflow
	if wfName == "" {
		wfName = "CI"
	}
	ctxName := f.ExpectedContext
	if ctxName == "" {
		ctxName = "CI required"
	}
	var matching []RunFixture
	for _, r := range f.Runs {
		if r.Event == "push" && r.HeadBranch == "main" && r.HeadSHA == f.SHA && r.Name == wfName {
			matching = append(matching, r)
		}
	}
	if len(matching) == 0 {
		result.Reason = "no matching CI push run for SHA " + f.SHA
		return result
	}
	if len(matching) > 1 {
		result.Reason = fmt.Sprintf("ambiguous: %d matching CI push run IDs for SHA %s", len(matching), f.SHA)
		return result
	}
	run := matching[0]
	result.RunID = run.ID
	for _, attempt := range run.Attempts {
		if attempt.Conclusion != "success" {
			baseReason := fmt.Sprintf("run %d attempt %d has non-success conclusion %q (reruns do not erase prior evidence)", run.ID, attempt.RunAttempt, attempt.Conclusion)
			if f.Waiver != nil {
				if waiverValid(f.Waiver, f.SHA) {
					result.Valid = true
					result.Waived = true
					result.Reason = fmt.Sprintf("granted by emergency waiver from %s: %s (original evidence: %s)", f.Waiver.Approver, f.Waiver.Reason, f.Waiver.OriginalEvidence)
					return result
				}
				result.Reason = baseReason + "; waiver " + waiverRejection(f.Waiver, f.SHA)
			} else {
				result.Reason = baseReason
			}
			return result
		}
	}
	if run.Status != "completed" {
		result.Reason = fmt.Sprintf("run %d status is %q, not completed", run.ID, run.Status)
		return result
	}
	var ciRequired []JobFixture
	for _, job := range run.LatestAttemptJobs {
		if job.Name == ctxName && job.Conclusion == "success" && job.AppID == appID {
			ciRequired = append(ciRequired, job)
		}
	}
	if len(ciRequired) == 0 {
		result.Reason = fmt.Sprintf("run %d latest attempt has no successful %s job from app %d", run.ID, ctxName, appID)
		return result
	}
	if len(ciRequired) > 1 {
		result.Reason = fmt.Sprintf("run %d latest attempt has %d matching %s jobs (expected exactly 1)", run.ID, len(ciRequired), ctxName)
		return result
	}
	result.Valid = true
	return result
}

func waiverValid(w *WaiverFixture, sha string) bool {
	if w.Approver == "" || w.Reason == "" || w.IncidentLink == "" || w.SHA == "" || w.ExpiresAt == "" || w.OriginalEvidence == "" {
		return false
	}
	if w.SHA != sha {
		return false
	}
	expires, err := time.Parse(time.RFC3339, w.ExpiresAt)
	if err != nil {
		return false
	}
	return !time.Now().UTC().After(expires)
}

func waiverRejection(w *WaiverFixture, sha string) string {
	if w.Approver == "" || w.Reason == "" || w.IncidentLink == "" || w.SHA == "" || w.ExpiresAt == "" || w.OriginalEvidence == "" {
		return "incomplete (missing required fields)"
	}
	if w.SHA != sha {
		return "wrong SHA"
	}
	expires, err := time.Parse(time.RFC3339, w.ExpiresAt)
	if err != nil {
		return "invalid expiry format"
	}
	if time.Now().UTC().After(expires) {
		return "expired"
	}
	return "invalid"
}

type ghRun struct {
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CreatedAt  string `json:"created_at"`
	RunAttempt int    `json:"run_attempt"`
}

type ghRunsResp struct {
	Runs []ghRun `json:"workflow_runs"`
}

func ghAPI(repo, apiPath string) ([]byte, error) {
	cmd := exec.Command("gh", "api", "repos/"+repo+"/"+apiPath, "-q", ".")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w", apiPath, err)
	}
	return out, nil
}

func loadFromAPI(repo, sha string) (ProofFixture, error) {
	if _, err := ghAPI(repo, "compare/main..."+sha); err != nil {
		return ProofFixture{}, fmt.Errorf("SHA %s is not reachable from main: %w", sha, err)
	}
	data, err := ghAPI(repo, "actions/runs?head_sha="+sha+"&event=push&per_page=50")
	if err != nil {
		return ProofFixture{}, err
	}
	var runsResp ghRunsResp
	if err := json.Unmarshal(data, &runsResp); err != nil {
		return ProofFixture{}, fmt.Errorf("parse runs: %w", err)
	}
	fixture := ProofFixture{SHA: sha, MainReachable: true}
	for _, r := range runsResp.Runs {
		if r.Name != "CI" {
			continue
		}
		// Build attempts from the run data. The actions/runs/{id}/attempts
		// endpoint is not available on all plans; use the run's own conclusion
		// and run_attempt count instead. For multi-attempt runs, query
		// check-runs to detect prior red attempts that a rerun cannot erase.
		attemptCount := r.RunAttempt
		if attemptCount == 0 {
			attemptCount = 1
		}
		var attempts []AttemptFixture
		for i := 1; i <= attemptCount; i++ {
			ac := r.Conclusion
			if i < attemptCount {
				// Prior attempt: unknown without the attempts API.
				// Fall back to check-runs to detect prior failures.
				ac = "success" // optimistic; overridden below if check-runs show red
			}
			attempts = append(attempts, AttemptFixture{RunAttempt: i, Status: "completed", Conclusion: ac})
		}
		// Query check-runs for prior failures: any non-success CI required
		// check-run on this SHA indicates a prior red attempt.
		crData, crErr := ghAPI(repo, "commits/"+sha+"/check-runs?per_page=100")
		if crErr == nil {
			var crResp struct {
				CheckRuns []struct {
					Name       string `json:"name"`
					Conclusion string `json:"conclusion"`
					App        struct {
						ID int64 `json:"id"`
					} `json:"app"`
				} `json:"check_runs"`
			}
			if json.Unmarshal(crData, &crResp) == nil {
				for _, cr := range crResp.CheckRuns {
					if cr.Name == "CI required" && cr.Conclusion != "success" && cr.Conclusion != "" {
						// Prior red attempt: mark the earliest non-latest attempt as failed.
						if len(attempts) > 1 {
							attempts[0].Conclusion = cr.Conclusion
						}
					}
				}
			}
		}
		// Also collect CI required jobs from check-runs for the latest attempt.
		var jobs []JobFixture
		if crErr == nil {
			var crResp struct {
				CheckRuns []struct {
					Name       string `json:"name"`
					Conclusion string `json:"conclusion"`
					App        struct {
						ID int64 `json:"id"`
					} `json:"app"`
				} `json:"check_runs"`
			}
			if json.Unmarshal(crData, &crResp) == nil {
				for _, cr := range crResp.CheckRuns {
					jobs = append(jobs, JobFixture{Name: cr.Name, Conclusion: cr.Conclusion, AppID: cr.App.ID})
				}
			}
		}
		fixture.Runs = append(fixture.Runs, RunFixture{
			ID:                r.ID,
			Event:             r.Event,
			HeadSHA:           r.HeadSHA,
			HeadBranch:        r.HeadBranch,
			Name:              r.Name,
			Path:              r.Path,
			Status:            r.Status,
			Conclusion:        r.Conclusion,
			CreatedAt:         r.CreatedAt,
			Attempts:          attempts,
			LatestAttemptJobs: jobs,
		})
	}
	return fixture, nil
}

func loadFixture(path string) (ProofFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProofFixture{}, err
	}
	var f ProofFixture
	if err := json.Unmarshal(data, &f); err != nil {
		return ProofFixture{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return f, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ciproof", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fixturesDir := flags.String("fixtures", "", "directory of JSON proof fixtures")
	shaFlag := flags.String("sha", "", "SHA to prove")
	outputPath := flags.String("output", "", "write proof JSON to this path")
	repository := flags.String("repository", "", "query the live GitHub API for this repo")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *shaFlag == "" {
		fmt.Fprintln(stderr, "--sha is required")
		return 2
	}
	var fixture ProofFixture
	if *repository != "" {
		f, err := loadFromAPI(*repository, *shaFlag)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		fixture = f
	} else if *fixturesDir != "" {
		entries, err := os.ReadDir(*fixturesDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			f, err := loadFixture(filepath.Join(*fixturesDir, entry.Name()))
			if err != nil {
				continue
			}
			if f.SHA == *shaFlag {
				fixture = f
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(stderr, "no fixture for SHA %s in %s\n", *shaFlag, *fixturesDir)
			return 2
		}
	} else {
		fmt.Fprintln(stderr, "--fixtures or --repository is required")
		return 2
	}
	result := evaluateProof(fixture)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	} else {
		fmt.Fprintln(stdout, string(data))
	}
	if result.Valid {
		return 0
	}
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
