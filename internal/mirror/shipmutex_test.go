package mirror

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShipMutex_MutualExclusion(t *testing.T) {
	dir := t.TempDir()
	m1, err := AcquireShipMutex(dir, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer m1.Release()
	if _, err := AcquireShipMutex(dir, 150*time.Millisecond); err == nil {
		t.Fatal("second acquire should time out while held")
	}
}

func TestShipMutex_ReleaseFrees(t *testing.T) {
	dir := t.TempDir()
	m1, err := AcquireShipMutex(dir, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	m2, err := AcquireShipMutex(dir, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	m2.Release()
}

func TestShipMutex_ReleaseIdempotent(t *testing.T) {
	dir := t.TempDir()
	m, _ := AcquireShipMutex(dir, 50*time.Millisecond)
	if err := m.Release(); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(); err != nil {
		t.Fatal("double release should be nil")
	}
}

// TestShipMutex_StaleLockfileTakeover exercises the portable fallback path
// directly (engine lock pattern).
func TestShipMutex_StaleLockfileTakeover(t *testing.T) {
	dir := t.TempDir()
	// Plant a lockfile with an ancient mtime (crashed holder).
	path := shipMutexPath(dir)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("9999 2020-01-01T00:00:00Z"), 0o644)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(path, old, old)

	m, err := AcquireShipMutex(dir, time.Second)
	if err != nil {
		t.Fatalf("stale takeover should succeed: %v", err)
	}
	defer m.Release()
}

// TestShipMutex_TokenFencedRelease: after a stale takeover, the OLD holder's
// release must not remove the new holder's lock.
func TestShipMutex_TokenFencedRelease(t *testing.T) {
	dir := t.TempDir()
	path := shipMutexPath(dir)
	os.MkdirAll(filepath.Dir(path), 0o755)

	old := &ShipMutex{path: path, isFallback: true, token: "old-token"}
	// New holder took over and wrote its token.
	os.WriteFile(path, []byte("new-token"), 0o644)
	if err := old.Release(); err != nil {
		t.Fatalf("fenced release: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("old holder's release removed the new holder's lockfile")
	}
}
