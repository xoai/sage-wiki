package compiler

import (
	"github.com/xoai/sage-wiki/internal/metrics"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// BackpressureController provides adaptive concurrency control.
// It replaces the fixed semaphore pattern with dynamic limit adjustment
// based on rate limit (429) responses from LLM providers.
//
// The semaphore channel is allocated once at maxParallel capacity and never
// resized. The effective concurrency limit is enforced via an in-flight
// counter guarded by a sync.Cond — goroutines block on the cond instead
// of spin-waiting when the effective limit is reached.
type BackpressureController struct {
	maxParallel  int
	currentLimit atomic.Int32
	inFlight     atomic.Int32
	sem          chan struct{}
	mu           sync.Mutex // protects backoff state and cond
	cond         *sync.Cond // signaled when in-flight decreases or limit increases

	consecutiveOK atomic.Int32
	backoffCount  int           // number of consecutive backoffs (for exponential calc)
	baseDelay     time.Duration // base delay for exponential backoff
	restoreAfter  int           // consecutive successes before doubling limit
}

// NewBackpressureController creates a controller with the given max concurrency.
// If maxParallel <= 0, defaults to 20.
func NewBackpressureController(maxParallel int) *BackpressureController {
	if maxParallel <= 0 {
		maxParallel = 20
	}
	bc := &BackpressureController{
		maxParallel:  maxParallel,
		sem:          make(chan struct{}, maxParallel),
		baseDelay:    time.Second,
		restoreAfter: 5,
	}
	bc.cond = sync.NewCond(&bc.mu)
	bc.currentLimit.Store(int32(maxParallel))
	metrics.GaugeNamed("compile_backpressure_limit").Set(int64(maxParallel))
	return bc
}

// Acquire blocks until a slot is available AND the in-flight count is below
// the current effective limit. Returns a release function that MUST be called
// when the work is done.
//
// Synchronization design: inFlight is an atomic.Int32 accessed BOTH under
// the mutex (here in Acquire, via CAS) and without it (in release, via Add).
// This is intentional — the release path runs on every completed request and
// avoids mutex overhead for a simple decrement+signal. The CAS in Acquire
// handles the resulting race: if release decrements inFlight between our
// Load and CompareAndSwap, the CAS fails harmlessly and we retry. This is
// safe because:
//   - CAS prevents double-counting (no goroutine can increment without winning the swap)
//   - The mutex serializes Acquire waiters against each other
//   - release's Add(-1) is atomic and always correct regardless of mutex state
//   - cond.Signal in release wakes exactly one Acquire waiter
//
// The alternative (guarding inFlight entirely under the mutex) would require
// release to acquire the mutex on every call, adding contention on the hot
// path. The atomic+CAS pattern trades a rare retry for zero-contention release.
func (bc *BackpressureController) Acquire() func() {
	// First, acquire from the fixed-capacity semaphore
	bc.sem <- struct{}{}

	// Then, wait until in-flight is below the dynamic limit.
	// Uses sync.Cond to block efficiently instead of spin-waiting.
	bc.mu.Lock()
	for {
		current := bc.inFlight.Load()
		limit := bc.currentLimit.Load()
		if current < limit {
			if bc.inFlight.CompareAndSwap(current, current+1) {
				// NOTE (P2-2): a release interleaving with this CAS can leave
				// the gauge stale by one (release writes, then this overwrites);
				// it self-corrects on the next transition — accepted over a lock.
				metrics.GaugeNamed("compile_backpressure_in_flight").Set(int64(current + 1))
				bc.mu.Unlock()
				break
			}
			continue // CAS failed, retry immediately
		}
		// Over the effective limit — wait for signal (release or limit increase)
		bc.cond.Wait()
	}

	return func() {
		metrics.GaugeNamed("compile_backpressure_in_flight").Set(int64(bc.inFlight.Add(-1)))
		<-bc.sem
		bc.cond.Signal() // wake one waiter blocked on the effective limit
	}
}

// OnSuccess signals a successful request. After restoreAfter consecutive
// successes, doubles the current limit (up to maxParallel).
func (bc *BackpressureController) OnSuccess() {
	n := bc.consecutiveOK.Add(1)
	if int(n) >= bc.restoreAfter {
		bc.mu.Lock()

		// Double the limit (multiplicative increase)
		current := bc.currentLimit.Load()
		newLimit := current * 2
		if newLimit > int32(bc.maxParallel) {
			newLimit = int32(bc.maxParallel)
		}
		if newLimit > current {
			bc.currentLimit.Store(newLimit)
			metrics.GaugeNamed("compile_backpressure_limit").Set(int64(newLimit))
			// Reset backoff count on recovery. This means a subsequent 429
			// starts backoff from scratch (2^0 = 1s base delay). This is
			// intentional: the system has proven it can handle the current
			// load level, so a new 429 likely signals a transient spike
			// rather than sustained overload. The alternative (preserving
			// backoffCount) would cause increasingly aggressive backoff
			// across recovery cycles, leading to chronic under-utilization.
			bc.backoffCount = 0
			bc.cond.Broadcast() // wake all waiters — limit increased
		}
		bc.consecutiveOK.Store(0)
		bc.mu.Unlock()
	}
}

// OnRateLimit signals a 429 response. Halves the current limit (minimum 1)
// and returns the backoff duration to sleep before retrying.
func (bc *BackpressureController) OnRateLimit() time.Duration {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	bc.consecutiveOK.Store(0)

	// Halve the limit (minimum 1)
	current := bc.currentLimit.Load()
	newLimit := current / 2
	if newLimit < 1 {
		newLimit = 1
	}
	bc.currentLimit.Store(newLimit)
	metrics.GaugeNamed("compile_backpressure_limit").Set(int64(newLimit))

	// Exponential backoff with jitter
	bc.backoffCount++
	delay := bc.baseDelay * time.Duration(math.Pow(2, float64(bc.backoffCount-1)))
	if delay > 60*time.Second {
		delay = 60 * time.Second // cap at 1 minute
	}
	// Add jitter: ±25%
	jitter := time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
	return jitter
}

// CurrentLimit returns the current effective concurrency limit.
func (bc *BackpressureController) CurrentLimit() int {
	return int(bc.currentLimit.Load())
}

// MaxParallel returns the configured maximum concurrency.
func (bc *BackpressureController) MaxParallel() int {
	return bc.maxParallel
}

// InFlight returns the current number of in-flight requests.
func (bc *BackpressureController) InFlight() int {
	return int(bc.inFlight.Load())
}
