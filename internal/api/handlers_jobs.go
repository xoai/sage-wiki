package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/linter"
)

var pathPattern = regexp.MustCompile(`[/\\]\S*[/\\]\S+`)

func (r *Router) handleJobSubmit(w http.ResponseWriter, req *http.Request) {
	kind := JobKind(req.PathValue("kind"))

	body, _ := io.ReadAll(io.LimitReader(req.Body, 10_000))
	if len(body) == 0 {
		body = []byte("{}")
	}

	var kindToSet JobKind
	var topic *string
	var maxSources int
	var compileOpts compiler.CompileOpts
	var lintReq jobLintRequest
	var lintPass string
	var lintFix bool

	switch kind {
	case "compile":
		cr, err := parseJobListRequest(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error(), nil)
			return
		}
		if cr.hasTopic() {
			kindToSet = JobCompileTopic
			s := *cr.Topic
			topic = &s
			if cr.MaxSources != nil {
				maxSources = *cr.MaxSources
			}
		} else {
			kindToSet = JobCompile
			if cr.DryRun != nil {
				compileOpts.DryRun = *cr.DryRun
			}
			if cr.Fresh != nil {
				compileOpts.Fresh = *cr.Fresh
			}
			if cr.Prune != nil {
				compileOpts.Prune = *cr.Prune
			}
		}
	case "lint":
		if err := json.Unmarshal(body, &lintReq); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, fmt.Sprintf("invalid request body: %v", err), nil)
			return
		}
		if lintReq.Pass != nil {
			lintPass = *lintReq.Pass
			if !linter.ValidPassName(lintPass) {
				writeError(w, http.StatusBadRequest, CodeInvalidArgument,
					fmt.Sprintf("unknown lint pass: %s", lintPass),
					map[string]any{"valid": linter.PassNames()})
				return
			}
		}
		lintFix = lintReq.Fix
		kindToSet = JobLint
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, fmt.Sprintf("unknown job kind: %s", kind), nil)
		return
	}

	// Idempotency: a replayed key returns the original job with no new
	// dispatch — the single highest-value case (prevents duplicate LLM
	// spend on client retry, INT-07).
	idemKey := req.Header.Get("Idempotency-Key")
	if idemKey != "" {
		if existingID, ok := r.jobs.lookupIdemKey(kindToSet, idemKey); ok {
			if existing, found := r.jobs.snapshot(existingID); found {
				w.Header().Set("Location", "/v1/jobs/"+existing.ID)
				w.Header().Set("X-Idempotent-Replay", "true")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(jobToResponse(&existing))
				return
			}
		}
	}

	// Concurrency gate + creation: atomic check-and-create for active kinds.
	var j *Job
	if _, ok := activeJobKinds[kindToSet]; ok {
		var active *Job
		j, active = r.jobs.tryCreateOrGetActive(kindToSet)
		if j == nil && active != nil {
			writeJSON(w, http.StatusConflict, errorEnvelope{Error: apiError{
				Code:    CodeConflict,
				Message: "A compile is already in progress",
				Details: map[string]any{"active_job_id": active.ID},
			}})
			return
		}
	} else {
		j = r.jobs.create(kindToSet)
	}

	// Idempotency.
	if idemKey != "" {
		r.jobs.storeIdemKey(kindToSet, idemKey, j.ID)
	}

	snap, _ := r.jobs.snapshot(j.ID)
	w.Header().Set("Location", "/v1/jobs/"+j.ID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(jobToResponse(&snap))

	if wf, ok := w.(interface{ Flush() }); ok {
		wf.Flush()
	}

	// Launch the job goroutine. The context is NOT derived from the request:
	// net/http cancels a request's context when the handler returns, which
	// would cancel every job the instant its 202 is sent (httptest never
	// reproduces this — caught by the live contract test).
	go r.runJob(j, kindToSet, topic, maxSources, compileOpts, lintPass, lintFix)
}

func (r *Router) runJob(j *Job, kind JobKind, topic *string, maxSources int, opts compiler.CompileOpts, lintPass string, lintFix bool) {
	ctx, cancel := contextWithJobTimeout(context.Background())
	r.jobs.mu.Lock()
	// A DELETE that landed before this goroutine started already won: do
	// not resurrect a cancelled job by marking it running.
	if j.Status == JobCancelled {
		r.jobs.mu.Unlock()
		cancel()
		return
	}
	j.cancel = cancel
	now := r.jobs.now()
	j.Status = JobRunning
	j.StartedAt = &now
	if kind == JobLint {
		j.Progress = map[string]any{"stage": "running"}
	}
	r.jobs.mu.Unlock()

	defer cancel()

	// Compile jobs mirror the shared Progress hub into job.Progress so
	// pollers see live progress without a second vocabulary (spec §Progress
	// integration). Sends are non-blocking; the hub drops on a full buffer.
	if r.progress != nil && (kind == JobCompile || kind == JobCompileTopic) {
		events, unsubscribe := r.progress.Subscribe(64)
		done := make(chan struct{})
		defer func() {
			close(done)
			unsubscribe()
		}()
		go func() {
			set := func(ev compiler.ProgressEvent) {
				r.jobs.mu.Lock()
				j.Progress = ev
				r.jobs.mu.Unlock()
			}
			for {
				select {
				case <-done:
					// Drain events already buffered before shutdown — the
					// runner emits its last events before runJob returns, and
					// exiting without draining would lose them (CI flake on
					// fast machines).
					for {
						select {
						case ev, ok := <-events:
							if !ok {
								return
							}
							set(ev)
						default:
							return
						}
					}
				case ev, ok := <-events:
					if !ok {
						return
					}
					set(ev)
				}
			}
		}()
	}

	var runErr error
	switch kind {
	case JobCompile:
		if r.jobRunner == nil {
			r.jobs.mu.Lock()
			finish := r.jobs.now()
			j.FinishedAt = &finish
			j.Status = JobFailed
			j.Error = &apiError{Code: CodeInternal, Message: "compile not configured"}
			r.jobs.mu.Unlock()
			return
		}
		result, err := r.jobRunner.RunCompile(ctx, r.projectDir, opts)
		finish := r.jobs.now()
		r.jobs.mu.Lock()
		j.FinishedAt = &finish
		if err != nil {
			if ctx.Err() != nil {
				j.Status = JobCancelled
			} else {
				j.Status = JobFailed
				j.Error = &apiError{Code: CodeInternal, Message: safeErrorMessage(err)}
			}
		} else if j.Status != JobCancelled {
			// A user DELETE may have won while the runner finished — the
			// client's cancelled verdict stands.
			j.Status = JobDone
			j.Result = result
		}
		r.jobs.mu.Unlock()
		runErr = err
	case JobCompileTopic:
		if r.jobRunner == nil {
			r.jobs.mu.Lock()
			finish := r.jobs.now()
			j.FinishedAt = &finish
			j.Status = JobFailed
			j.Error = &apiError{Code: CodeInternal, Message: "compile-on-demand not configured"}
			r.jobs.mu.Unlock()
			return
		}
		odOpts := compiler.OnDemandOpts{
			Topic:      *topic,
			MaxSources: maxSources,
			ProjectDir: r.projectDir,
		}
		if maxSources <= 0 {
			odOpts.MaxSources = 20
		} else if maxSources > 200 {
			odOpts.MaxSources = 200
		}
		result, err := r.jobRunner.RunCompileTopic(ctx, odOpts)
		finish := r.jobs.now()
		r.jobs.mu.Lock()
		j.FinishedAt = &finish
		if err != nil {
			if ctx.Err() != nil {
				j.Status = JobCancelled
			} else {
				j.Status = JobFailed
				j.Error = &apiError{Code: CodeInternal, Message: safeErrorMessage(err)}
			}
		} else if j.Status != JobCancelled {
			// A user DELETE may have won while the runner finished — the
			// client's cancelled verdict stands.
			j.Status = JobDone
			j.Result = result
		}
		r.jobs.mu.Unlock()
		runErr = err
	case JobLint:
		if r.jobRunner == nil {
			r.jobs.mu.Lock()
			finish := r.jobs.now()
			j.FinishedAt = &finish
			j.Status = JobFailed
			j.Error = &apiError{Code: CodeInternal, Message: "lint not configured"}
			r.jobs.mu.Unlock()
			return
		}
		lintCtx := &linter.LintContext{
			ProjectDir: r.projectDir,
		}
		results, err := r.jobRunner.RunLint(ctx, lintCtx, lintPass, lintFix)
		finish := r.jobs.now()
		r.jobs.mu.Lock()
		j.FinishedAt = &finish
		if err != nil {
			if ctx.Err() != nil {
				j.Status = JobCancelled
			} else {
				j.Status = JobFailed
				j.Error = &apiError{Code: CodeInternal, Message: safeErrorMessage(err)}
			}
		} else if j.Status != JobCancelled {
			j.Status = JobDone
			j.Result = results
			j.Progress = map[string]any{"stage": "done"}
		}
		r.jobs.mu.Unlock()
		runErr = err
	}

	if runErr != nil && ctx.Err() != nil {
		log.Printf("api: job %s (%s) cancelled", j.ID, kind)
	} else if runErr != nil {
		log.Printf("api: job %s (%s) failed: %v", j.ID, kind, runErr)
	} else {
		log.Printf("api: job %s (%s) completed", j.ID, kind)
	}
}

func (r *Router) handleJobGet(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	j, ok := r.jobs.snapshot(id)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "job not found", nil)
		return
	}
	writeJSON(w, http.StatusOK, jobToResponse(&j))
}

func (r *Router) handleJobList(w http.ResponseWriter, req *http.Request) {
	statusFilter := JobStatus(req.URL.Query().Get("status"))
	if statusFilter != "" && !validJobStatuses[statusFilter] {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument,
			fmt.Sprintf("invalid status filter: %s", statusFilter),
			map[string]any{"valid": []string{"pending", "running", "done", "failed", "cancelled"}},
		)
		return
	}
	jobs := r.jobs.list(statusFilter)
	type listResponse struct {
		Jobs []jobResponse `json:"jobs"`
	}
	resp := listResponse{Jobs: make([]jobResponse, len(jobs))}
	for i := range jobs {
		resp.Jobs[i] = jobToResponse(&jobs[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

func (r *Router) handleJobDelete(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")

	r.jobs.mu.Lock()
	j, ok := r.jobs.getLocked(id)
	if !ok {
		r.jobs.mu.Unlock()
		writeError(w, http.StatusNotFound, CodeNotFound, "job not found", nil)
		return
	}
	if j.Finished() {
		r.jobs.mu.Unlock()
		writeError(w, http.StatusConflict, CodeConflict, "job already finished", nil)
		return
	}

	if j.cancel != nil {
		j.cancel()
	}
	finish := r.jobs.now()
	j.Status = JobCancelled
	j.FinishedAt = &finish
	snap := *j
	r.jobs.mu.Unlock()

	writeJSON(w, http.StatusAccepted, jobToResponse(&snap))
}

// contextWithJobTimeout returns a context with a generous deadline so
// a job goroutine is not unbounded, but normal compiles finish naturally.
func contextWithJobTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 2*time.Hour)
}

func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if pathPattern.MatchString(msg) {
		log.Printf("api: path-leaking error suppressed: %s", msg)
		return "compile failed — check server logs"
	}
	return msg
}
