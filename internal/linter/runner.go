package linter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/storage"
)

// Severity levels for findings.
const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"
)

// Finding represents a single lint issue.
type Finding struct {
	Pass     string `json:"pass"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// LintPass defines the interface for pluggable lint passes.
type LintPass interface {
	Name() string
	Run(ctx *LintContext) ([]Finding, error)
	CanAutoFix() bool
	Fix(ctx *LintContext, findings []Finding) error
}

// LintContext provides access to project data for lint passes.
type LintContext struct {
	ProjectDir     string
	OutputDir      string
	DBPath         string
	DB             *storage.DB // shared DB connection (optional — opened from DBPath if nil)
	ValidRelations []string    // valid ontology relation type names
}

// LintResult holds the aggregated output of a lint run.
type LintResult struct {
	Findings []Finding
	PassName string
	Duration time.Duration
}

// Runner orchestrates lint passes.
type Runner struct {
	passes []LintPass
}

// NewRunner creates a runner with all registered passes.
func NewRunner() *Runner {
	return &Runner{
		passes: []LintPass{
			&CompletenessPass{},
			&StylePass{},
			&OrphansPass{},
			&ConsistencyPass{},
			&ConnectionsPass{},
			&ImputePass{},
			&StalenessPass{},
		},
	}
}

// EnsureDB opens the DB if not already provided in context.
// Returns a cleanup function that closes the DB only if we opened it.
func (ctx *LintContext) EnsureDB() (func(), error) {
	if ctx.DB != nil {
		return func() {}, nil // already provided
	}
	if ctx.DBPath == "" {
		return func() {}, nil
	}
	db, err := storage.Open(ctx.DBPath)
	if err != nil {
		return func() {}, err
	}
	ctx.DB = db
	return func() { db.Close() }, nil
}

// Run executes lint passes. If passName is empty, runs all passes.
func (r *Runner) Run(ctx *LintContext, passName string, fix bool) ([]LintResult, error) {
	cleanup, err := ctx.EnsureDB()
	if err != nil {
		return nil, fmt.Errorf("lint: open db: %w", err)
	}
	defer cleanup()

	var results []LintResult

	for _, pass := range r.passes {
		if passName != "" && pass.Name() != passName {
			continue
		}

		start := time.Now()
		log.Info("lint pass starting", "pass", pass.Name())

		findings, err := pass.Run(ctx)
		if err != nil {
			log.Error("lint pass failed", "pass", pass.Name(), "error", err)
			continue
		}

		duration := time.Since(start)

		if fix && pass.CanAutoFix() && len(findings) > 0 {
			log.Info("auto-fixing", "pass", pass.Name(), "findings", len(findings))
			if err := pass.Fix(ctx, findings); err != nil {
				log.Error("auto-fix failed", "pass", pass.Name(), "error", err)
			}
		}

		results = append(results, LintResult{
			Findings: findings,
			PassName: pass.Name(),
			Duration: duration,
		})

		log.Info("lint pass complete", "pass", pass.Name(), "findings", len(findings), "duration", duration)
	}

	return results, nil
}

// SaveReport writes lint results to .sage/lintlog/.
func SaveReport(projectDir string, results []LintResult) error {
	logDir := filepath.Join(projectDir, ".sage", "lintlog")
	os.MkdirAll(logDir, 0755)

	timestamp := time.Now().Format("20060102-150405")

	// JSON report
	jsonPath := filepath.Join(logDir, fmt.Sprintf("lint-%s.json", timestamp))
	data, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		return err
	}

	// Human-readable report
	txtPath := filepath.Join(logDir, fmt.Sprintf("lint-%s.txt", timestamp))
	var report string
	totalFindings := 0
	for _, r := range results {
		report += fmt.Sprintf("=== %s (%s) ===\n", r.PassName, r.Duration)
		for _, f := range r.Findings {
			report += fmt.Sprintf("  [%s] %s", f.Severity, f.Message)
			if f.Path != "" {
				report += fmt.Sprintf(" (%s)", f.Path)
			}
			report += "\n"
			totalFindings++
		}
		if len(r.Findings) == 0 {
			report += "  No issues found.\n"
		}
		report += "\n"
	}
	report += fmt.Sprintf("Total: %d findings\n", totalFindings)

	return os.WriteFile(txtPath, []byte(report), 0644)
}

// FormatFindings renders findings as a human-readable string.
func FormatFindings(results []LintResult) string {
	var out string
	total := 0
	for _, r := range results {
		if len(r.Findings) > 0 {
			out += fmt.Sprintf("%s: %d findings\n", r.PassName, len(r.Findings))
			for _, f := range r.Findings {
				out += fmt.Sprintf("  [%s] %s", f.Severity, f.Message)
				if f.Path != "" {
					out += fmt.Sprintf(" (%s)", f.Path)
				}
				out += "\n"
			}
			total += len(r.Findings)
		}
	}
	if total == 0 {
		out = "No issues found.\n"
	} else {
		out += fmt.Sprintf("\nTotal: %d findings\n", total)
	}
	return out
}
