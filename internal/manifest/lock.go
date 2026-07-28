package manifest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

// tokenCounter makes each acquirer's lock token unique within a process.
var tokenCounter atomic.Uint64

// lockOptions tunes the advisory-lock timing. The only invariant that matters
// for correctness is heartbeatInterval << staleThreshold: a live holder must
// refresh its mtime often enough that a waiter never sees it as stale. Tests
// shrink these; production uses defaultLockOptions.
type lockOptions struct {
	staleThreshold    time.Duration // reclaim a lock whose mtime is older than this (crashed holder)
	heartbeatInterval time.Duration // refresh our own lock mtime this often while holding
	retryInterval     time.Duration // poll interval while waiting to acquire
	timeout           time.Duration // give up acquiring after this (bounded block)
}

func defaultLockOptions() lockOptions {
	// Ordering matters: heartbeatInterval << staleThreshold < timeout. The
	// heartbeat keeps a live holder well under the stale threshold; and because a
	// waiter's timeout EXCEEDS the stale threshold, an orphaned lock left with a
	// fresh mtime (e.g. a holder killed just after writing it) is reclaimed by
	// takeover before any waiter spuriously times out.
	return lockOptions{
		staleThreshold:    90 * time.Second,
		heartbeatInterval: 30 * time.Second,
		retryInterval:     50 * time.Millisecond,
		timeout:           120 * time.Second,
	}
}

// manifestLock is a held advisory lock over a manifest file. Release it with
// Unlock. It is NOT reentrant and must be released by the goroutine tree that
// acquired it.
type manifestLock struct {
	path  string        // the lock file path (manifest path + ".lock")
	token string        // our unique holder token, written into the lock file
	stop  chan struct{} // closed by Unlock to stop the heartbeat
	done  chan struct{} // closed by the heartbeat goroutine when it exits
}

func newHeldLock(lockPath, token string, opts lockOptions) *manifestLock {
	l := &manifestLock{
		path:  lockPath,
		token: token,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go l.heartbeat(opts.heartbeatInterval)
	return l
}

// acquireLock blocks until it holds the advisory lock for manifestPath, ctx is
// cancelled, or the default timeout elapses.
func acquireLock(ctx context.Context, manifestPath string) (*manifestLock, error) {
	return acquireLockOpts(ctx, manifestPath, defaultLockOptions())
}

// WithLock runs fn while holding the exclusive manifest lock, without loading or
// saving the manifest. The reconciler uses it to serialize an individual
// file/DB repair against other manifest writers (D5 lock-per-repair) — the scan
// itself stays lock-free.
func WithLock(ctx context.Context, manifestPath string, fn func() error) error {
	lock, err := acquireLock(ctx, manifestPath)
	if err != nil {
		return fmt.Errorf("manifest.WithLock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

// lockContended reports whether a failed exclusive-create means "another
// acquirer holds it" rather than a genuine error worth aborting for.
//
// POSIX signals contention with EEXIST. Windows signals it two other ways that
// os.IsExist does NOT match: ERROR_ACCESS_DENIED when the target sits in a
// pending-delete state (a rival's Unlock removed it while our Create was in
// flight) and ERROR_SHARING_VIOLATION when another handle is still open. Both
// surface through Go as fs.ErrPermission. Reporting either as fatal aborts the
// caller's whole Mutate and silently drops the update it was making — observed
// as TestIngestConcurrentNoLostSource failing on windows-latest with
// "acquire lock ...: Access is denied" followed by "lost update: expected 10
// sources, got 9".
//
// The Windows widening is bounded: a real permission problem still fails, just
// via the acquire timeout (which reports the underlying error) instead of
// immediately.
func lockContended(err error, goos string) bool {
	if errors.Is(err, fs.ErrExist) {
		return true
	}
	return goos == "windows" && errors.Is(err, fs.ErrPermission)
}

// acquireLockOpts is acquireLock with explicit timing (used by tests).
func acquireLockOpts(ctx context.Context, manifestPath string, opts lockOptions) (*manifestLock, error) {
	lockPath := manifestPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("manifest: create lock dir: %w", err)
	}

	// Each acquirer has a unique token, written into the lock file. It fences
	// stale takeover: two waiters over a crashed holder's lock can't both win,
	// and Unlock never removes a lock that a takeover handed to someone else.
	token := fmt.Sprintf("%d-%d", os.Getpid(), tokenCounter.Add(1))
	// The settle window lets any concurrent takeover writes land before we verify
	// which token survived; it only applies on the (rare) crash-recovery path.
	settle := 3 * opts.retryInterval
	if settle < 10*time.Millisecond {
		settle = 10 * time.Millisecond
	}

	deadline := time.Now().Add(opts.timeout)
	// Kept so a timeout can name the error we retried on — the difference
	// between "someone held it" and a real permission problem.
	var lastContendErr error
	for {
		// Fast path: create the lock exclusively. Only one creator wins.
		if f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
			_, _ = f.WriteString(token)
			f.Close()
			return newHeldLock(lockPath, token, opts), nil
		} else if !lockContended(err, runtime.GOOS) {
			return nil, fmt.Errorf("manifest: acquire lock %s: %w", lockPath, err)
		} else {
			lastContendErr = err
		}

		// The lock exists. If it is stale (a crashed holder no longer heartbeating
		// — a live holder's heartbeat keeps mtime fresh, so this never fires for
		// one), attempt a token takeover: write our token, let concurrent takeover
		// writers settle, then verify OUR token survived. Exactly one contender's
		// token wins, so only one takes over — no double-acquire, and a live holder
		// is never overwritten because it is never seen as stale.
		//
		// Residual (accepted): a portable file lock cannot be perfectly race-free
		// on the crash-recovery path — if a contender stalls for longer than the
		// settle window between observing staleness and writing its token, it can
		// clobber a takeover winner. This needs both a genuine crash AND a
		// >settle-window scheduler stall between two adjacent syscalls, so it is
		// vanishingly rare; the alternative (OS flock) sacrifices the network-FS
		// portability this lock is built for.
		if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > opts.staleThreshold {
			if wf, werr := os.OpenFile(lockPath, os.O_WRONLY|os.O_TRUNC, 0600); werr == nil {
				_, _ = wf.WriteString(token)
				wf.Close()
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("manifest: acquire lock %s: %w", lockPath, ctx.Err())
				case <-time.After(settle):
				}
				if cur, rerr := os.ReadFile(lockPath); rerr == nil && string(cur) == token {
					return newHeldLock(lockPath, token, opts), nil
				}
				// Lost the takeover race — another token survived; wait below.
			}
		}

		// Held by a live holder (or a takeover winner). Wait, honoring ctx and timeout.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("manifest: acquire lock %s: %w", lockPath, ctx.Err())
		case <-time.After(opts.retryInterval):
		}
		if time.Now().After(deadline) {
			if lastContendErr != nil {
				return nil, fmt.Errorf("manifest: timed out acquiring lock %s after %s (last: %w)",
					lockPath, opts.timeout, lastContendErr)
			}
			return nil, fmt.Errorf("manifest: timed out acquiring lock %s after %s", lockPath, opts.timeout)
		}
	}
}

// heartbeat refreshes the lock file's mtime while the lock is held, so a waiter
// does not mistake a slow-but-live holder for a crashed one. It exits when stop
// is closed and signals that via done.
func (l *manifestLock) heartbeat(interval time.Duration) {
	defer close(l.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			now := time.Now()
			_ = os.Chtimes(l.path, now, now) // best-effort; a missed beat only shortens the live window
		}
	}
}

// Unlock stops the heartbeat and removes the lock file. It waits for the
// heartbeat goroutine to exit first (so no refresh races the removal), then
// removes the file only if it still carries OUR token — so we never delete a
// lock that a stale takeover legitimately handed to a new holder. (The window
// for that is tiny: we heartbeated within the last interval, so no waiter has
// yet seen us as stale.)
func (l *manifestLock) Unlock() error {
	close(l.stop)
	<-l.done
	if cur, err := os.ReadFile(l.path); err != nil || string(cur) != l.token {
		return nil // already lost/replaced — not ours to remove
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("manifest: release lock %s: %w", l.path, err)
	}
	return nil
}
