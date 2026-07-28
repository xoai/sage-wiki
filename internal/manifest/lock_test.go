package manifest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastLockOpts makes lock timing test-fast while preserving the ordering that
// matters: heartbeat << staleThreshold so a live holder is never reclaimed.
func fastLockOpts() lockOptions {
	return lockOptions{
		staleThreshold:    100 * time.Millisecond,
		heartbeatInterval: 15 * time.Millisecond,
		retryInterval:     5 * time.Millisecond,
		timeout:           2 * time.Second,
	}
}

func TestLockBlocksUntilRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	l1, err := acquireLockOpts(context.Background(), path, fastLockOpts())
	if err != nil {
		t.Fatalf("acquire l1: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		l2, err := acquireLockOpts(context.Background(), path, fastLockOpts())
		if err != nil {
			t.Errorf("acquire l2: %v", err)
			return
		}
		close(acquired)
		_ = l2.Unlock()
	}()

	// While l1 is held, l2 must not acquire.
	select {
	case <-acquired:
		t.Fatal("l2 acquired while l1 still held — lock did not block")
	case <-time.After(50 * time.Millisecond):
	}

	// Release l1; l2 should now acquire promptly.
	if err := l1.Unlock(); err != nil {
		t.Fatalf("unlock l1: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(1 * time.Second):
		t.Fatal("l2 did not acquire after l1 released")
	}
}

func TestLockCtxCancelReturnsPromptly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	l1, err := acquireLockOpts(context.Background(), path, fastLockOpts())
	if err != nil {
		t.Fatalf("acquire l1: %v", err)
	}
	defer l1.Unlock()

	// A long timeout so that a prompt return proves ctx-cancel, not timeout.
	opts := fastLockOpts()
	opts.timeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = acquireLockOpts(ctx, path, opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled acquire, got nil")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("ctx-cancel took %s — did not return promptly (timeout was %s)", elapsed, opts.timeout)
	}
}

func TestLockStaleReclaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")
	lockPath := path + ".lock"

	// Simulate a crashed holder: a lock file with an mtime well past the stale
	// threshold and no live heartbeat.
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatalf("plant stale lock: %v", err)
	}
	old := time.Now().Add(-1 * time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age lock: %v", err)
	}

	l, err := acquireLockOpts(context.Background(), path, fastLockOpts())
	if err != nil {
		t.Fatalf("expected stale lock to be reclaimed, got: %v", err)
	}
	if err := l.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// TestLockStaleReclaimMutualExclusion is the regression for the non-atomic
// reclaim race: with a stale lock already present, many contenders racing to
// acquire must never both hold it. A plain Stat->Remove->Create reclaim lets two
// waiters both take the lock (and one delete the other's fresh lock); the atomic
// rename-aside reclaim must serialize them.
func TestLockStaleReclaimMutualExclusion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")
	lockPath := path + ".lock"

	// Plant a stale (crashed-holder) lock.
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatalf("plant stale lock: %v", err)
	}
	old := time.Now().Add(-1 * time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age lock: %v", err)
	}

	opts := fastLockOpts()
	opts.timeout = 3 * time.Second

	var held atomic.Int32
	var maxHeld atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := acquireLockOpts(context.Background(), path, opts)
			if err != nil {
				return // timed out contending — acceptable, just not a double-hold
			}
			n := held.Add(1)
			for {
				m := maxHeld.Load()
				if n <= m || maxHeld.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			held.Add(-1)
			_ = l.Unlock()
		}()
	}
	wg.Wait()

	if maxHeld.Load() > 1 {
		t.Fatalf("two goroutines held the lock simultaneously (max=%d) — stale-reclaim double-acquire", maxHeld.Load())
	}
}

func TestLockLiveHolderNotReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	// A live holder with a short stale threshold but an active heartbeat.
	l1, err := acquireLockOpts(context.Background(), path, fastLockOpts())
	if err != nil {
		t.Fatalf("acquire l1: %v", err)
	}

	// Wait well past the stale threshold. The heartbeat must keep l1 alive.
	time.Sleep(4 * fastLockOpts().staleThreshold)

	// A waiter with a bounded timeout must NOT reclaim the live holder.
	opts := fastLockOpts()
	opts.timeout = 150 * time.Millisecond
	if _, err := acquireLockOpts(context.Background(), path, opts); err == nil {
		t.Fatal("waiter reclaimed a LIVE holder — heartbeat failed to protect it")
	}

	// After release, a fresh acquire succeeds.
	if err := l1.Unlock(); err != nil {
		t.Fatalf("unlock l1: %v", err)
	}
	l2, err := acquireLockOpts(context.Background(), path, fastLockOpts())
	if err != nil {
		t.Fatalf("acquire l2 after release: %v", err)
	}
	_ = l2.Unlock()
}

// TestLockContentionClassification pins the portable contention predicate.
//
// A contended exclusive-create must be retried, never reported as fatal:
// treating it as fatal aborts the caller's Mutate and silently loses its
// update. POSIX signals contention with EEXIST; Windows signals it with
// ERROR_ACCESS_DENIED (target pending-delete while Unlock's Remove is in
// flight) or ERROR_SHARING_VIOLATION (another handle open) — both of which
// surface as fs.ErrPermission and neither of which os.IsExist matches.
// Regression: TestIngestConcurrentNoLostSource failing on windows-latest with
// "acquire lock ...: Access is denied" → "lost update: expected 10, got 9".
func TestLockContentionClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		goos string
		want bool
	}{
		{"eexist on linux", fs.ErrExist, "linux", true},
		{"eexist on windows", fs.ErrExist, "windows", true},
		{"wrapped eexist", fmt.Errorf("open x: %w", fs.ErrExist), "linux", true},
		{"access denied on windows is contention", fs.ErrPermission, "windows", true},
		{"wrapped access denied on windows", fmt.Errorf("open x: %w", fs.ErrPermission), "windows", true},
		{"permission denied on linux is NOT contention", fs.ErrPermission, "linux", false},
		{"unrelated error is never contention", errors.New("disk on fire"), "windows", false},
		{"not-exist is never contention", fs.ErrNotExist, "windows", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lockContended(tc.err, tc.goos); got != tc.want {
				t.Errorf("lockContended(%v, %q) = %v, want %v", tc.err, tc.goos, got, tc.want)
			}
		})
	}
}

// TestLockConcurrentAcquireNoLostAcquisition drives the fast path under real
// contention: every goroutine must eventually hold the lock exactly once, and
// none may fail because a rival held it at the moment it tried.
func TestLockConcurrentAcquireNoLostAcquisition(t *testing.T) {
	dir := t.TempDir()
	mfPath := filepath.Join(dir, ".manifest.json")

	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	var held int
	errs := make([]error, 0, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := fastLockOpts()
			opts.timeout = 10 * time.Second
			// A live holder must never look stale while 11 rivals spin on it.
			opts.staleThreshold = time.Minute
			lock, err := acquireLockOpts(context.Background(), mfPath, opts)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			mu.Lock()
			held++
			mu.Unlock()
			_ = lock.Unlock()
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Errorf("contended acquire must retry, not fail: %d error(s), first: %v", len(errs), errs[0])
	}
	if held != n {
		t.Errorf("lost acquisition: %d of %d goroutines held the lock", held, n)
	}
}
