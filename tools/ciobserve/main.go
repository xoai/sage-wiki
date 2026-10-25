package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// JobObservation is a single job within a workflow run.
type JobObservation struct {
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	StartedAt      string  `json:"started_at"`
	CompletedAt    string  `json:"completed_at"`
	QueueSeconds   float64 `json:"queue_seconds,omitempty"`
	RuntimeSeconds float64 `json:"runtime_seconds"`
	Conclusion     string  `json:"conclusion"`
	RunnerOS       string  `json:"runner_os"`
}

// RunObservation is a complete workflow run with its jobs.
type RunObservation struct {
	Workflow     string           `json:"workflow"`
	RunID        int64            `json:"run_id"`
	Event        string           `json:"event"`
	Attempt      int              `json:"attempt"`
	HeadSHA      string           `json:"head_sha"`
	HeadBranch   string           `json:"head_branch"`
	CheckoutSHA  string           `json:"checkout_sha"`
	CreatedAt    string           `json:"created_at"`
	Status       string           `json:"status"`
	Conclusion   string           `json:"conclusion"`
	DagRevision  string           `json:"dag_revision,omitempty"`
	Jobs         []JobObservation `json:"jobs"`
	FirstFailure *JobObservation  `json:"first_failure,omitempty"`
}

// Divergence records a mismatch between current and candidate outcomes on the same source tree.
type Divergence struct {
	HeadSHA         string `json:"head_sha"`
	CurrentResult   string `json:"current_result"`
	CandidateResult string `json:"candidate_result"`
	Explanation     string `json:"explanation,omitempty"`
}

// QualificationRecord is the persisted qualification verdict.
type QualificationRecord struct {
	CandidateWorkflow string       `json:"candidate_workflow"`
	CurrentWorkflow   string       `json:"current_workflow"`
	SampleCount       int          `json:"sample_count"`
	DateSpanDays      float64      `json:"date_span_days"`
	EarliestRun       string       `json:"earliest_run"`
	LatestRun         string       `json:"latest_run"`
	Status            string       `json:"status"`
	Reason            string       `json:"reason,omitempty"`
	P50Seconds        float64      `json:"p50_seconds"`
	P95Seconds        float64      `json:"p95_seconds"`
	RunnerOccupancy   float64      `json:"runner_occupancy_seconds"`
	Divergences       []Divergence `json:"divergences,omitempty"`
	Mutations         []string     `json:"mutations,omitempty"`
	OwnerDisposition  string       `json:"owner_disposition"`
	GeneratedAt       string       `json:"generated_at"`
}

// QualificationOptions controls the qualification thresholds.
type QualificationOptions struct {
	MinimumRuns   int
	MinimumDays   int
	MaxP95Seconds float64
}

// wallTime returns the runtime in seconds for a run (max job completed - min job started).
func (r RunObservation) wallTime() float64 {
	if len(r.Jobs) == 0 {
		return 0
	}
	var starts, ends []time.Time
	for _, job := range r.Jobs {
		s, err1 := time.Parse(time.RFC3339, job.StartedAt)
		e, err2 := time.Parse(time.RFC3339, job.CompletedAt)
		if err1 != nil || err2 != nil {
			continue
		}
		starts = append(starts, s)
		ends = append(ends, e)
	}
	if len(starts) == 0 {
		return 0
	}
	minStart := starts[0]
	maxEnd := ends[0]
	for _, t := range starts[1:] {
		if t.Before(minStart) {
			minStart = t
		}
	}
	for _, t := range ends[1:] {
		if t.After(maxEnd) {
			maxEnd = t
		}
	}
	return maxEnd.Sub(minStart).Seconds()
}

func evaluateQualification(runs []RunObservation, opts QualificationOptions) QualificationRecord {
	rec := QualificationRecord{
		CurrentWorkflow:  "CI",
		OwnerDisposition: "pending",
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	// Live fixtures carry started_at/completed_at but not runtime_seconds;
	// derive it so runner occupancy reflects reality rather than zero.
	for i := range runs {
		for j := range runs[i].Jobs {
			job := &runs[i].Jobs[j]
			if job.RuntimeSeconds != 0 {
				continue
			}
			st, err1 := time.Parse(time.RFC3339, job.StartedAt)
			en, err2 := time.Parse(time.RFC3339, job.CompletedAt)
			if err1 == nil && err2 == nil && en.After(st) {
				job.RuntimeSeconds = en.Sub(st).Seconds()
			}
		}
	}

	// Group runs by workflow, then match by head_sha.
	currentBySHA := map[string]RunObservation{}
	candidateBySHA := map[string]RunObservation{}
	candidateWfName := ""
	for _, run := range runs {
		if run.Workflow == "CI" {
			currentBySHA[run.HeadSHA] = run
		} else {
			candidateBySHA[run.HeadSHA] = run
			if candidateWfName == "" {
				candidateWfName = run.Workflow
			}
		}
	}
	rec.CandidateWorkflow = candidateWfName

	// Matched pairs: head_sha present in both workflows, both completed.
	type matchedPair struct {
		sha       string
		current   RunObservation
		candidate RunObservation
	}
	var pairs []matchedPair
	for sha, current := range currentBySHA {
		candidate, exists := candidateBySHA[sha]
		if !exists {
			continue
		}
		if current.Status != "completed" || candidate.Status != "completed" {
			continue
		}
		pairs = append(pairs, matchedPair{sha: sha, current: current, candidate: candidate})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].current.CreatedAt < pairs[j].current.CreatedAt
	})

	rec.SampleCount = len(pairs)
	if len(pairs) > 0 {
		rec.EarliestRun = pairs[0].current.CreatedAt
		rec.LatestRun = pairs[len(pairs)-1].current.CreatedAt
		if earliest, err := time.Parse(time.RFC3339, rec.EarliestRun); err == nil {
			if latest, err := time.Parse(time.RFC3339, rec.LatestRun); err == nil {
				rec.DateSpanDays = latest.Sub(earliest).Hours() / 24
			}
		}
	}

	// Check run count.
	if rec.SampleCount < opts.MinimumRuns {
		rec.Status = "insufficient"
		rec.Reason = fmt.Sprintf("sample count %d below minimum %d", rec.SampleCount, opts.MinimumRuns)
		return rec
	}

	// Check date span.
	if rec.DateSpanDays < float64(opts.MinimumDays) {
		rec.Status = "insufficient"
		rec.Reason = fmt.Sprintf("date span %.1f days below minimum %d", rec.DateSpanDays, opts.MinimumDays)
		return rec
	}

	// Shared-responsibility scoping (2026-10-24 disposition lesson): the two
	// workflows deliberately differ in coverage — CI carries vuln scan,
	// frontend dist verify, and fuzz that the shadow does not duplicate.
	// A whole-run conclusion mismatch caused by those CI-only jobs is a
	// COVERAGE difference, not a candidate-quality divergence (the live
	// window flagged exactly this shape twice: 3669810, d8ca516). Divergence
	// is therefore job-scoped:
	//   - a conclusion mismatch on a job BOTH workflows run, either direction;
	//   - a failure on ANY candidate job (the shadow exists to prove itself —
	//     its regressions are alert-worthy even without a current counterpart).
	// Current-only job failures are coverage, ignored by design.
	var wallTimes []float64
	var totalOccupancy float64
	for _, p := range pairs {
		if len(p.candidate.Jobs) == 0 {
			rec.Status = "insufficient"
			rec.Reason = "candidate run " + p.sha + " has no jobs (vacuous)"
			return rec
		}
		currentByName := map[string]string{}
		for _, job := range p.current.Jobs {
			currentByName[job.Name] = job.Conclusion
		}
		candidateByName := map[string]string{}
		for _, job := range p.candidate.Jobs {
			candidateByName[job.Name] = job.Conclusion
		}
		shared := 0
		for name, cand := range candidateByName {
			cur, both := currentByName[name]
			if !both {
				continue
			}
			shared++
			if cur != cand {
				rec.Divergences = append(rec.Divergences, Divergence{
					HeadSHA:         p.sha,
					CurrentResult:   cur,
					CandidateResult: cand,
					Explanation:     fmt.Sprintf("shared job %q: current=%s candidate=%s", name, cur, cand),
				})
			}
		}
		if shared == 0 {
			rec.Status = "insufficient"
			rec.Reason = "candidate run " + p.sha + " shares no job names with the current run — vacuous comparison"
			return rec
		}
		// Candidate-only regressions: the shadow exists to prove itself, so a
		// failure in a job the current workflow lacks is still alert-worthy.
		// Shared both-red agreement on a genuinely red tree is not — the
		// mismatch path above owns shared disagreements.
		for name, cand := range candidateByName {
			_, sharedJob := currentByName[name]
			if cand == "failure" && !sharedJob {
				rec.Divergences = append(rec.Divergences, Divergence{
					HeadSHA:         p.sha,
					CurrentResult:   p.current.Conclusion,
					CandidateResult: p.candidate.Conclusion,
					Explanation:     fmt.Sprintf("candidate job %q failed", name),
				})
			}
		}
		wt := p.candidate.wallTime()
		if wt > 0 {
			wallTimes = append(wallTimes, wt)
		}
		for _, job := range p.candidate.Jobs {
			totalOccupancy += job.RuntimeSeconds
		}
		if p.candidate.Event == "" {
			rec.Status = "insufficient"
			rec.Reason = "candidate run " + p.sha + " has no event metadata"
			return rec
		}
	}
	if len(rec.Divergences) > 0 {
		sort.Slice(rec.Divergences, func(i, j int) bool {
			return rec.Divergences[i].HeadSHA < rec.Divergences[j].HeadSHA
		})
		rec.Status = "divergent"
		rec.Reason = fmt.Sprintf("%d shared-responsibility divergence(s)", len(rec.Divergences))
		return rec
	}

	// Compute p50/p95.
	rec.P50Seconds = percentile(wallTimes, 0.50)
	rec.P95Seconds = percentile(wallTimes, 0.95)
	rec.RunnerOccupancy = totalOccupancy / float64(len(pairs))

	// Check p95 bound.
	if rec.P95Seconds > opts.MaxP95Seconds {
		rec.Status = "insufficient"
		rec.Reason = fmt.Sprintf("p95 %.0fs exceeds bound %.0fs", rec.P95Seconds, opts.MaxP95Seconds)
		return rec
	}

	rec.Status = "qualified"
	rec.OwnerDisposition = "qualified"
	rec.Reason = ""
	return rec
}

// RunMetric is one run's latency attribution inside a baseline.
type RunMetric struct {
	HeadSHA                string  `json:"head_sha"`
	Event                  string  `json:"event"`
	Branch                 string  `json:"head_branch"`
	DagRevision            string  `json:"dag_revision"`
	WallSeconds            float64 `json:"wall_seconds"`
	RunnerOccupancySeconds float64 `json:"runner_occupancy_seconds"`
	MaxStartDelaySeconds   float64 `json:"max_start_delay_seconds"` // started_at - run created_at: queue + dependency wait
	SlowestJob             string  `json:"slowest_job"`
	SlowestJobSeconds      float64 `json:"slowest_job_seconds"`
	JobCount               int     `json:"job_count"`
	FirstActionableFailure string  `json:"first_actionable_failure,omitempty"`
}

// MetricsRecord is the promoted-DAG latency baseline (Milestone 6 Task 22).
// Wall time is the job EXECUTION span (max job end - min job start), never
// queue-inclusive: queue delay is recorded separately so an iteration that
// only moves waiting around cannot masquerade as an execution win.
type MetricsRecord struct {
	Workflow                  string      `json:"workflow"`
	Status                    string      `json:"status"`
	Reason                    string      `json:"reason,omitempty"`
	Runs                      []RunMetric `json:"runs"`
	MedianWallSeconds         float64     `json:"median_wall_seconds"`
	P95WallSeconds            float64     `json:"p95_wall_seconds"`
	AggregateOccupancySeconds float64     `json:"aggregate_occupancy_seconds"`
	MedianStartDelaySeconds   float64     `json:"median_start_delay_seconds"` // start delay: queue AND dependency wait; NOT queue health
	SlowestJob                string      `json:"slowest_job"`
	SlowestJobSeconds         float64     `json:"slowest_job_seconds"`
	GeneratedAt               string      `json:"generated_at"`
}

// MetricsOptions controls baseline evaluation thresholds.
type MetricsOptions struct {
	MinimumRuns int
}

func evaluateMetrics(runs []RunObservation, opts MetricsOptions) MetricsRecord {
	rec := MetricsRecord{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	valid := 0
	for _, run := range runs {
		if run.Workflow == "" {
			rec.Workflow = "CI"
		} else if rec.Workflow == "" {
			rec.Workflow = run.Workflow
		}
		if run.Conclusion != "success" || run.Status != "completed" {
			rec.Status = "not-success"
			rec.Reason = "run " + run.HeadSHA + " conclusion " + run.Conclusion + " — baseline requires successful runs only"
			return rec
		}
		if len(run.Jobs) == 0 {
			rec.Status = "missing-jobs"
			rec.Reason = "run " + run.HeadSHA + " has no job observations"
			return rec
		}
		valid++
	}
	if valid < opts.MinimumRuns {
		rec.Status = "insufficient"
		rec.Reason = fmt.Sprintf("%d comparable runs below minimum %d", valid, opts.MinimumRuns)
		return rec
	}
	if runs[0].DagRevision == "" {
		rec.Status = "mixed-dag"
		rec.Reason = "runs carry no DAG revision — comparability unproven"
		return rec
	}
	rev := runs[0].DagRevision
	wfName := runs[0].Workflow
	for _, run := range runs[1:] {
		if run.DagRevision != rev {
			rec.Status = "mixed-dag"
			rec.Reason = "runs span multiple DAG revisions (" + rev + ", " + run.DagRevision + ") — not comparable"
			return rec
		}
		if run.Workflow != wfName {
			rec.Status = "mixed-dag"
			rec.Reason = "runs span multiple workflows (" + wfName + ", " + run.Workflow + ") — not comparable"
			return rec
		}
	}

	var walls, queues []float64
	for _, run := range runs {
		rm := RunMetric{
			HeadSHA:     run.HeadSHA,
			Event:       run.Event,
			Branch:      run.HeadBranch,
			DagRevision: run.DagRevision,
			JobCount:    len(run.Jobs),
		}
		if run.FirstFailure != nil {
			rm.FirstActionableFailure = run.FirstFailure.Name
		}
		rm.WallSeconds = run.wallTime() // execution span, queue-excluded
		var occ float64
		for _, job := range run.Jobs {
			occ += job.RuntimeSeconds
			if job.RuntimeSeconds > rm.SlowestJobSeconds {
				rm.SlowestJobSeconds = job.RuntimeSeconds
				rm.SlowestJob = job.Name
			}
			if job.QueueSeconds > rm.MaxStartDelaySeconds {
				rm.MaxStartDelaySeconds = job.QueueSeconds
			}
			queues = append(queues, job.QueueSeconds)
		}
		rm.RunnerOccupancySeconds = occ
		rec.AggregateOccupancySeconds += occ
		walls = append(walls, rm.WallSeconds)
		if rm.SlowestJobSeconds > rec.SlowestJobSeconds {
			rec.SlowestJobSeconds = rm.SlowestJobSeconds
			rec.SlowestJob = rm.SlowestJob
		}
		rec.Runs = append(rec.Runs, rm)
	}
	rec.MedianWallSeconds = percentile(walls, 0.50)
	rec.P95WallSeconds = percentile(walls, 0.95)
	rec.MedianStartDelaySeconds = percentile(queues, 0.50)
	rec.Status = "ok"
	return rec
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func loadFromFixtures(dir string) ([]RunObservation, error) {
	var all []RunObservation
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var runs []RunObservation
		if err := json.Unmarshal(data, &runs); err != nil {
			// Try single object.
			var single RunObservation
			if err2 := json.Unmarshal(data, &single); err2 != nil {
				return nil, fmt.Errorf("%s: %w", entry.Name(), err)
			}
			runs = []RunObservation{single}
		}
		all = append(all, runs...)
	}
	return all, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ciobserve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fixturesDir := flags.String("fixtures", "", "directory of JSON run fixtures to evaluate")
	metricsRepo := flags.String("repository", "", "live metrics mode: repository (owner/name)")
	metricsWorkflow := flags.String("workflow", "CI", "live metrics mode: workflow name")
	metricsWorkflowFile := flags.String("workflow-file", ".github/workflows/ci.yml", "live metrics mode: DAG revision file (blob sha per run commit)")
	metricsRuns := flags.Int("runs", 5, "live metrics mode: minimum comparable successful runs")
	outputPath := flags.String("output", "", "write qualification JSON to this path")
	minRuns := flags.Int("minimum-runs", 20, "minimum matched run pairs for qualification")
	minDays := flags.Int("minimum-days", 7, "minimum date span in days for qualification")
	maxP95 := flags.Float64("max-p95-seconds", 480, "maximum p95 wall time in seconds")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *metricsRepo != "" {
		return runMetrics(*metricsRepo, *metricsWorkflow, *metricsWorkflowFile, *metricsRuns, *outputPath, stdout, stderr)
	}

	if *fixturesDir == "" {
		fmt.Fprintln(stderr, "--fixtures is required")
		return 2
	}

	runs, err := loadFromFixtures(*fixturesDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(runs) == 0 {
		fmt.Fprintln(stderr, "no runs loaded from fixtures")
		return 2
	}

	opts := QualificationOptions{
		MinimumRuns:   *minRuns,
		MinimumDays:   *minDays,
		MaxP95Seconds: *maxP95,
	}
	rec := evaluateQualification(runs, opts)

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, data, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	} else {
		fmt.Fprintln(stdout, string(data))
	}

	// Exit contract (fail-closed): a scheduled observer must not alert on
	// its own healthy intermediate state. "insufficient" — the window is
	// still accumulating pairs — is that state: exit 0 so the daily run is
	// green and the persist step commits the accumulating verdict.
	// "divergent" — same-tree conclusion mismatch between current and
	// candidate — is the one observation a scheduled observer exists to
	// alert on: exit 1. Any other status string (including the zero value
	// if a future refactor adds an early return) is unexpected: exit 2,
	// never a silent green.
	switch rec.Status {
	case "qualified", "insufficient":
		return 0
	case "divergent":
		return 1
	default:
		return 2
	}
}

// ghAPIJSON runs `gh api` and decodes JSON into out.
func ghAPIJSON(path string, out any) error {
	cmd := exec.Command("gh", "api", path, "-q", ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh api %s: %v: %s", path, err, stderr.String())
	}
	return json.Unmarshal(stdout, out)
}

// runMetrics fetches the N most recent successful push runs of one workflow,
// pins each to its DAG revision (workflow-file blob sha at the run's commit),
// evaluates the latency baseline, and writes it. Exit contract mirrors the
// qualification mode: ok exits 0; every rejection exits 1 (a baseline
// evaluator must not silently green on incomparable data).
func runMetrics(repo, workflow, workflowFile string, minRuns int, outputPath string, stdout, stderr io.Writer) int {
	var wfList struct {
		Workflows []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"workflows"`
	}
	if err := ghAPIJSON("repos/"+repo+"/actions/workflows?per_page=100", &wfList); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var wfID int64
	for _, wf := range wfList.Workflows {
		if wf.Name == workflow {
			wfID = wf.ID
			break
		}
	}
	if wfID == 0 {
		fmt.Fprintf(stderr, "workflow %q not found in %s\n", workflow, repo)
		return 2
	}
	var runsResp struct {
		WorkflowRuns []struct {
			RunObservation
			ID int64 `json:"id"` // the runs API names it id, not run_id
		} `json:"workflow_runs"`
	}
	if err := ghAPIJSON(fmt.Sprintf("repos/%s/actions/workflows/%d/runs?event=push&status=success&per_page=%d", repo, wfID, minRuns), &runsResp); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runs := make([]RunObservation, 0, len(runsResp.WorkflowRuns))
	for i := range runsResp.WorkflowRuns {
		runs = append(runs, runsResp.WorkflowRuns[i].RunObservation)
		runs[i].RunID = runsResp.WorkflowRuns[i].ID
	}
	for i := range runs {
		run := &runs[i]
		run.Workflow = workflow
		var jobsResp struct {
			Jobs []JobObservation `json:"jobs"`
		}
		if err := ghAPIJSON(fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100", repo, run.RunID), &jobsResp); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if len(jobsResp.Jobs) == 100 {
			// Fail closed rather than silently truncating: occupancy and wall
			// spans would understate with no rejection.
			fmt.Fprintf(stderr, "run %d has >= 100 jobs — pagination required before a baseline can be trusted\n", run.RunID)
			return 2
		}
		run.Jobs = jobsResp.Jobs
		for j := range run.Jobs {
			job := &run.Jobs[j]
			if job.RuntimeSeconds == 0 {
				st, err1 := time.Parse(time.RFC3339, job.StartedAt)
				en, err2 := time.Parse(time.RFC3339, job.CompletedAt)
				if err1 == nil && err2 == nil && en.After(st) {
					job.RuntimeSeconds = en.Sub(st).Seconds()
				}
			}
			// Start delay (started_at - created_at): includes queue AND
			// dependency wait; recorded as its own metric, never folded
			// into wall time.
			if job.QueueSeconds == 0 && job.StartedAt != "" {
				if created, err := time.Parse(time.RFC3339, run.CreatedAt); err == nil {
					if st, err2 := time.Parse(time.RFC3339, job.StartedAt); err2 == nil && st.After(created) {
						job.QueueSeconds = st.Sub(created).Seconds()
					}
				}
			}
		}
		var content struct {
			SHA string `json:"sha"`
		}
		if err := ghAPIJSON(fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, workflowFile, run.HeadSHA), &content); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		run.DagRevision = content.SHA[:12]
	}
	rec := evaluateMetrics(runs, MetricsOptions{MinimumRuns: minRuns})
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if outputPath != "" {
		if err := os.WriteFile(outputPath, data, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	} else {
		fmt.Fprintln(stdout, string(data))
	}
	if rec.Status != "ok" {
		fmt.Fprintf(stderr, "metrics baseline rejected: %s (%s)\n", rec.Status, rec.Reason)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
