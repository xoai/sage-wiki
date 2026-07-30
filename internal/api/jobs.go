package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

type JobKind string

const (
	JobCompile      JobKind = "compile"
	JobCompileTopic JobKind = "compile_topic"
	JobLint         JobKind = "lint"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobDone      JobStatus = "done"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// validJobStatuses is the set of statuses accepted by the ?status= filter.
var validJobStatuses = map[JobStatus]bool{
	JobPending:   true,
	JobRunning:   true,
	JobDone:      true,
	JobFailed:    true,
	JobCancelled: true,
}

// activeJobKinds are kinds that serialise (only one active at a time).
var activeJobKinds = map[JobKind]bool{
	JobCompile:      true,
	JobCompileTopic: true,
}

// Job represents an async operation submitted through the /v1/jobs API.
type Job struct {
	ID          string     `json:"job_id"`
	Kind        JobKind    `json:"kind"`
	Status      JobStatus  `json:"status"`
	SubmittedAt time.Time  `json:"submitted_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Progress    any        `json:"progress,omitempty"`
	Result      any        `json:"result,omitempty"`
	Error       *apiError  `json:"error,omitempty"`
	cancel      func()
}

// Active returns whether the job is still in-flight (pending or running).
func (j *Job) Active() bool {
	return j.Status == JobPending || j.Status == JobRunning
}

// Finished returns whether the job reached a terminal state.
func (j *Job) Finished() bool {
	return j.Status == JobDone || j.Status == JobFailed || j.Status == JobCancelled
}

const jobStoreMax = 100

type jobStore struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	order []string
	idem  map[string]string // Idempotency-Key → job_id (per-kind scoped)
	now   func() time.Time
}

func newJobStore() *jobStore {
	return &jobStore{
		jobs: map[string]*Job{},
		idem: map[string]string{},
		now:  time.Now,
	}
}

func newJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Printf("api: crypto/rand.Read failed, falling back to time-based id: %v", err)
		// Best-effort fallback: timestamp + nonce avoids collision in practice.
		ts := time.Now().UnixNano()
		return fmt.Sprintf("%x-%x", ts, ts>>32)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // UUIDv4
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func (s *jobStore) create(kind JobKind) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(kind)
}

func (s *jobStore) createLocked(kind JobKind) *Job {
	j := &Job{
		ID:          newJobID(),
		Kind:        kind,
		Status:      JobPending,
		SubmittedAt: s.now(),
	}
	s.jobs[j.ID] = j
	s.order = append(s.order, j.ID)
	for len(s.order) > jobStoreMax {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.jobs, oldest)
		// Clean stale idempotency key entries for the evicted job.
		for k, v := range s.idem {
			if v == oldest {
				delete(s.idem, k)
			}
		}
	}
	return j
}

// tryCreateOrGetActive atomically checks for an active job of a serialising
// kind and creates a new one if none exists. Compile and compile_topic share
// one gate (spec §Concurrency: any active compile kind conflicts) — lint is
// ungated. Returns (newJob, nil) on success, or (nil, activeJob) if an
// active job already holds the gate.
func (s *jobStore) tryCreateOrGetActive(kind JobKind) (*Job, *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := activeJobKinds[kind]; ok {
		for _, id := range s.order {
			j, ok := s.jobs[id]
			if !ok {
				continue
			}
			if _, gated := activeJobKinds[j.Kind]; gated && j.Active() {
				return nil, j
			}
		}
	}
	return s.createLocked(kind), nil
}

func (s *jobStore) get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

// snapshot returns a copy of the job under the lock — safe to read after
// the lock is released (HTTP responses must not read the live *Job while
// the job goroutine mutates it).
func (s *jobStore) snapshot(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// getLocked requires a write lock held; returns the job for concurrent-safe mutation.
func (s *jobStore) getLocked(id string) (*Job, bool) {
	j, ok := s.jobs[id]
	return j, ok
}

func (s *jobStore) list(status JobStatus) []Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Iterate in insertion order (oldest first) so callers can rely on
	// deterministic ordering; we reverse at collection time for the
	// standard "most recent first" list view.
	pending := make([]Job, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		j, ok := s.jobs[s.order[i]]
		if !ok {
			continue
		}
		if status != "" && j.Status != status {
			continue
		}
		pending = append(pending, *j)
	}
	return pending
}

func (s *jobStore) activeJobOfKind(kind JobKind) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		j, ok := s.jobs[id]
		if !ok {
			continue
		}
		if j.Kind == kind && j.Active() {
			return j, true
		}
	}
	return nil, false
}

func (s *jobStore) storeIdemKey(kind JobKind, key, jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idem[fmt.Sprintf("%s:%s", kind, key)] = jobID
}

func (s *jobStore) lookupIdemKey(kind JobKind, key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.idem[fmt.Sprintf("%s:%s", kind, key)]
	return id, ok
}

// jobResponse is the envelope for a single job in API responses.
type jobResponse struct {
	JobID       string     `json:"job_id"`
	Kind        JobKind    `json:"kind"`
	Status      JobStatus  `json:"status"`
	SubmittedAt time.Time  `json:"submitted_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Progress    any        `json:"progress,omitempty"`
	Result      any        `json:"result,omitempty"`
	Error       *apiError  `json:"error,omitempty"`
}

func jobToResponse(j *Job) jobResponse {
	return jobResponse{
		JobID:       j.ID,
		Kind:        j.Kind,
		Status:      j.Status,
		SubmittedAt: j.SubmittedAt,
		StartedAt:   j.StartedAt,
		FinishedAt:  j.FinishedAt,
		Progress:    j.Progress,
		Result:      j.Result,
		Error:       j.Error,
	}
}

// jobListRequest is the parsed body for POST /v1/jobs/compile requests.
// Flags are pointers so presence (not truthiness) selects full-compile mode —
// `{"dry_run": false}` is an explicit full compile, `{}` is a 400.
type jobListRequest struct {
	Topic      *string `json:"topic,omitempty"`
	MaxSources *int    `json:"max_sources,omitempty"`
	DryRun     *bool   `json:"dry_run,omitempty"`
	Fresh      *bool   `json:"fresh,omitempty"`
	Prune      *bool   `json:"prune,omitempty"`
}

// jobLintRequest is the parsed body for POST /v1/jobs/lint requests.
type jobLintRequest struct {
	Pass *string `json:"pass,omitempty"`
	Fix  bool    `json:"fix,omitempty"`
}

// hasTopic reports whether the request specifies topic-compile mode.
func (r jobListRequest) hasTopic() bool { return r.Topic != nil }

// hasCompileFlags reports whether any full-compile flag is present.
func (r jobListRequest) hasCompileFlags() bool {
	return r.DryRun != nil || r.Fresh != nil || r.Prune != nil
}

func parseJobListRequest(body []byte) (jobListRequest, error) {
	var req jobListRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return req, fmt.Errorf("invalid request body: %w", err)
		}
	}
	if req.hasTopic() && req.hasCompileFlags() {
		return req, fmt.Errorf("exactly one of 'topic' or compile flags expected")
	}
	if !req.hasTopic() && !req.hasCompileFlags() {
		return req, fmt.Errorf("either 'topic' or compile flags required")
	}
	return req, nil
}
