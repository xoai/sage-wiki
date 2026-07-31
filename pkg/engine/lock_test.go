package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEngine_ConcurrentOpenSameDir is AC-B4's lock half: a second exclusive
// acquire on the same directory fails fast with ErrLocked (flock path on
// unix, lockfile elsewhere — acquireLock picks the platform impl).
func TestEngine_ConcurrentOpenSameDir(t *testing.T) {
	dir := t.TempDir()
	l1, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire = %v, want ErrLocked", err)
	}
	if err := l1.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Released: a new acquire succeeds.
	l2, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.release()
}

// TestLockfileFallback exercises the portable path directly: contention,
// release, and stale takeover (no chmod tricks — staleness via Chtimes,
// which works on every platform).
func TestLockfileFallback(t *testing.T) {
	dir := t.TempDir()

	l1, err := acquireLockfile(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := acquireLockfile(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("contended acquire = %v, want ErrLocked", err)
	}
	if err := releaseLockfile(l1); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
		t.Error("release must remove the lockfile")
	}

	// Stale takeover: a lockfile older than fallbackStaleAfter is taken over.
	l2, err := acquireLockfile(dir)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	stale := time.Now().Add(-2 * fallbackStaleAfter)
	if err := os.Chtimes(lockPath(dir), stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	l3, err := acquireLockfile(dir)
	if err != nil {
		t.Fatalf("stale takeover must succeed: %v", err)
	}
	releaseLockfile(l2)
	releaseLockfile(l3)
}

// TestLockPathInsideSageDir pins the lock location (SPEC-01: the lock lives
// with the workspace data, not at the root).
func TestLockPathInsideSageDir(t *testing.T) {
	got := lockPath("/tmp/ws")
	want := filepath.Join("/tmp/ws", ".sage", "engine.lock")
	if got != want {
		t.Errorf("lockPath = %q, want %q", got, want)
	}
}
