package compiler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/pkg/events"
)

var errDocBudgetDeadline = errors.New("compiler: document budget deadline")

// DocBudget tracks one document's compile_doc_timeout stopwatch
// (SPEC-08 D6). The budget is consumed ONLY while the doc's own LLM units
// run — queueing time and cross-document phases (concept extraction,
// article writes, batch mode) never consume it. Each unit's context
// deadline derives from the REMAINING budget.
type DocBudget struct {
	mu    sync.Mutex
	limit time.Duration
	used  time.Duration
}

// NewDocBudget returns a budget of the given size. limit <= 0 means "no
// per-doc bound" — Remaining reports a sentinel year so unit contexts stay
// effectively unbounded (callers normally skip wiring in that case).
func NewDocBudget(limit time.Duration) *DocBudget {
	if limit <= 0 {
		limit = 365 * 24 * time.Hour
	}
	return &DocBudget{limit: limit}
}

// Remaining returns the unconsumed budget; <= 0 means exhausted.
func (b *DocBudget) Remaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.used
}

// Expired reports whether the budget is exhausted.
func (b *DocBudget) Expired() bool {
	return b.Remaining() <= 0
}

// Consume records elapsed unit time.
func (b *DocBudget) Consume(d time.Duration) {
	if d < 0 {
		return
	}
	b.mu.Lock()
	b.used += d
	b.mu.Unlock()
}

// UnitContext derives the deadline for one LLM unit: the minimum of the
// parent's remaining deadline and the remaining budget (context nesting
// takes the minimum). An exhausted budget yields an already-done context.
func (b *DocBudget) UnitContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	rem := b.Remaining()
	return context.WithTimeoutCause(parent, rem, errDocBudgetDeadline)
}

// finishUnit records elapsed unit time and reports whether the exact context
// deadline created by UnitContext caused unitErr. The cause, rather than a
// second elapsed-time sample, distinguishes document budget expiry from parent
// cancellation and nested provider timeouts.
func (b *DocBudget) finishUnit(unitCtx context.Context, elapsed time.Duration, unitErr error) bool {
	budgetOwned := unitCtx != nil &&
		errors.Is(context.Cause(unitCtx), errDocBudgetDeadline) &&
		(errors.Is(unitErr, context.DeadlineExceeded) || errors.Is(unitErr, context.Canceled))

	b.mu.Lock()
	if elapsed >= 0 {
		b.used += elapsed
	}
	if budgetOwned && b.used < b.limit {
		b.used = b.limit
	}
	b.mu.Unlock()
	return budgetOwned
}

// Limit returns the configured budget size (for typed-error reporting).
func (b *DocBudget) Limit() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit
}

// Used returns the consumed time (for typed-error reporting).
func (b *DocBudget) Used() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// DocBudgets is the per-run registry of per-doc budgets (SPEC-08 D6).
// One budget per doc path, created lazily on first unit start.
type DocBudgets struct {
	mu    sync.Mutex
	limit time.Duration
	m     map[string]*DocBudget
}

// NewDocBudgets returns a registry whose budgets each get the given limit.
func NewDocBudgets(limit time.Duration) *DocBudgets {
	return &DocBudgets{limit: limit, m: make(map[string]*DocBudget)}
}

// For returns (creating on first use) the budget for one doc.
func (r *DocBudgets) For(doc string) *DocBudget {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.m[doc]
	if !ok {
		b = NewDocBudget(r.limit)
		r.m[doc] = b
	}
	return b
}

// docTimeoutError is the typed error for an exhausted per-doc budget
// (SPEC-08 AC11): a LimitError unwrapping to limits.ErrTimeout.
func docTimeoutError(b *DocBudget) *limits.LimitError {
	return limits.New(limits.WhichCompileDocTimeout,
		int64(b.Limit()/time.Millisecond), int64(b.Used()/time.Millisecond))
}

// emitDocTimeout fans the SPEC-08 AC11 event pair for a doc whose budget
// expired: compile_doc_finished{Skipped:true} (the existing payload shape
// carries no outcome enum — the limit_exceeded event IS the timeout signal)
// plus limit_exceeded{Which:"compile_doc_timeout"}. No sink = no-op.
func emitDocTimeout(sink events.Sink, projectDir, jobID, docID string, err error) {
	if sink == nil {
		return
	}
	ws := filepath.Base(projectDir)
	sink.Emit(events.NewEvent(ws, events.TypeCompileDocFinished, events.CompileDocFinished{
		JobID:   jobID,
		DocID:   docID,
		Skipped: true,
	}))
	var le *limits.LimitError
	if errors.As(err, &le) {
		sink.Emit(events.NewEvent(ws, events.TypeLimitExceeded, events.LimitExceeded{
			Which:  le.Which,
			Limit:  le.Limit,
			Got:    le.Got,
			Detail: "compile_doc_timeout:" + docID,
		}))
		return
	}
	sink.Emit(events.NewEvent(ws, events.TypeLimitExceeded, events.LimitExceeded{
		Which:  limits.WhichCompileDocTimeout,
		Detail: "compile_doc_timeout:" + docID,
	}))
}
