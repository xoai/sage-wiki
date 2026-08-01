package mirror

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalState_MissingIsFresh(t *testing.T) {
	s, err := LoadLocalState(filepath.Join(t.TempDir(), "mirror-local.json"))
	if err != nil {
		t.Fatalf("LoadLocalState missing: %v", err)
	}
	if s.Generation != 0 || s.PendingRotation || s.ConsecutiveDefers != 0 {
		t.Fatalf("fresh state = %+v", s)
	}
}

func TestLocalState_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror-local.json")
	want := &LocalState{
		Generation:        3,
		WALSalt:           12345,
		WALOffset:         4096,
		LastDBSHA256:      "ab",
		LastDBSize:        8192,
		LastSegmentSeq:    7,
		LastRotationAt:    time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		PendingRotation:   true,
		ConsecutiveDefers: 2,
	}
	if err := SaveLocalState(path, want); err != nil {
		t.Fatalf("SaveLocalState: %v", err)
	}
	got, err := LoadLocalState(path)
	if err != nil {
		t.Fatalf("LoadLocalState: %v", err)
	}
	if *got != *want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

func TestLocalState_CorruptIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror-local.json")
	os.WriteFile(path, []byte("{garbage"), 0o644)
	if _, err := LoadLocalState(path); err == nil {
		t.Fatal("corrupt state should error (loud, not silent reset)")
	}
}

// TestLocalState_AtomicWrite: a crash before rename leaves the OLD file
// intact (temp+rename pattern).
func TestLocalState_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mirror-local.json")
	old := &LocalState{Generation: 1}
	if err := SaveLocalState(path, old); err != nil {
		t.Fatal(err)
	}
	// Simulate crash: a temp file exists but rename never happened.
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte("{partial"), 0o644)
	got, err := LoadLocalState(path)
	if err != nil || got.Generation != 1 {
		t.Fatalf("old state not intact after crash-before-rename: %+v %v", got, err)
	}
	// Next save succeeds despite the orphan temp.
	if err := SaveLocalState(path, &LocalState{Generation: 2}); err != nil {
		t.Fatalf("save over orphan tmp: %v", err)
	}
	got, _ = LoadLocalState(path)
	if got.Generation != 2 {
		t.Fatalf("state = %d", got.Generation)
	}
}
