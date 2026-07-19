package manifest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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
	return lockOptions{
		staleThreshold:    120 * time.Second,
		heartbeatInterval: 30 * time.Second,
		retryInterval:     50 * time.Millisecond,
		timeout:           60 * time.Second,
	}
}

// manifestLock is a held advisory lock over a manifest file. Release it with
// Unlock. It is NOT reentrant and must be released by the goroutine tree that
// acquired it.
type manifestLock struct {
	path string        // the lock file path (manifest path + ".lock")
	stop chan struct{} // closed by Unlock to stop the heartbeat
	done chan struct{} // closed by the heartbeat goroutine when it exits
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

// acquireLockOpts is acquireLock with explicit timing (used by tests).
func acquireLockOpts(ctx context.Context, manifestPath string, opts lockOptions) (*manifestLock, error) {
	lockPath := manifestPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("manifest: create lock dir: %w", err)
	}

	deadline := time.Now().Add(opts.timeout)
	for {
		// Reclaim a stale lock (a crashed holder no longer refreshing its mtime).
		// A live holder's heartbeat keeps mtime fresh, so this never fires for it.
		if info, err := os.Stat(lockPath); err == nil {
			if time.Since(info.ModTime()) > opts.staleThreshold {
				os.Remove(lockPath) // best-effort reclaim
			}
		}

		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			l := &manifestLock{
				path: lockPath,
				stop: make(chan struct{}),
				done: make(chan struct{}),
			}
			go l.heartbeat(opts.heartbeatInterval)
			return l, nil
		}
		if !os.IsExist(err) {
			// A real error (permissions, missing dir) — not contention.
			return nil, fmt.Errorf("manifest: acquire lock %s: %w", lockPath, err)
		}

		// Held by someone else. Wait, honoring ctx cancellation and the timeout.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("manifest: acquire lock %s: %w", lockPath, ctx.Err())
		case <-time.After(opts.retryInterval):
		}
		if time.Now().After(deadline) {
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
// heartbeat goroutine to exit before removing so no refresh can race the
// removal (which would recreate/leave a stray lock).
func (l *manifestLock) Unlock() error {
	close(l.stop)
	<-l.done
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("manifest: release lock %s: %w", l.path, err)
	}
	return nil
}
