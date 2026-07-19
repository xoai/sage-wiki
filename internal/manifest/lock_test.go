package manifest

import (
	"context"
	"os"
	"path/filepath"
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
