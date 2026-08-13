package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
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

	// Check for identical-tree divergence (before success checks: a candidate
	// failure where the current succeeded IS the divergence we look for).
	for _, p := range pairs {
		if p.current.Conclusion != p.candidate.Conclusion {
			rec.Divergences = append(rec.Divergences, Divergence{
				HeadSHA:         p.sha,
				CurrentResult:   p.current.Conclusion,
				CandidateResult: p.candidate.Conclusion,
			})
		}
	}
	if len(rec.Divergences) > 0 {
		rec.Status = "divergent"
		rec.Reason = fmt.Sprintf("%d unexplained identical-tree divergence(s)", len(rec.Divergences))
		return rec
	}

	// Check for vacuous candidate jobs, require success, and collect wall times.
	var wallTimes []float64
	var totalOccupancy float64
	for _, p := range pairs {
		if len(p.candidate.Jobs) == 0 {
			rec.Status = "insufficient"
			rec.Reason = "candidate run " + p.sha + " has no jobs (vacuous)"
			return rec
		}
		if p.candidate.Conclusion != "success" {
			rec.Status = "insufficient"
			rec.Reason = "candidate run " + p.sha + " did not succeed (" + p.candidate.Conclusion + ")"
			return rec
		}
		if p.current.Conclusion != "success" {
			rec.Status = "insufficient"
			rec.Reason = "current run " + p.sha + " did not succeed (" + p.current.Conclusion + ")"
			return rec
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
	outputPath := flags.String("output", "", "write qualification JSON to this path")
	minRuns := flags.Int("minimum-runs", 20, "minimum matched run pairs for qualification")
	minDays := flags.Int("minimum-days", 7, "minimum date span in days for qualification")
	maxP95 := flags.Float64("max-p95-seconds", 480, "maximum p95 wall time in seconds")
	if err := flags.Parse(args); err != nil {
		return 2
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

	if rec.Status == "qualified" {
		return 0
	}
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
