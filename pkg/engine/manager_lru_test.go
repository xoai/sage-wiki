package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// openCountHook is the Manager-internal observability the spec names for
// lock/eviction assertions (engine.lock file presence is NOT a hold signal
// on flock platforms — lock_unix.go leaves the file behind after release).
func (m *Manager) openCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.handles)
}

func TestManager_LRUEviction(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "w1")
	initWorkspaceIn(t, root, "w2")
	initWorkspaceIn(t, root, "w3")

	m, err := OpenManager(context.Background(), root, WithMaxOpen(2))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	if _, err := m.Workspace(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond) // distinct lastUse timestamps
	if _, err := m.Workspace(ctx, "w2"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := m.Workspace(ctx, "w3"); err != nil {
		t.Fatal(err)
	}
	if got := m.openCount(); got != 2 {
		t.Fatalf("openCount = %d, want 2", got)
	}
	// w1 was least recently used → evicted: re-opening it must not error,
	// and it must evict w2 (now oldest).
	if _, err := m.Workspace(ctx, "w1"); err != nil {
		t.Fatalf("re-open w1 after eviction: %v", err)
	}
	// Recency refresh: w3 touched after w2 → w2 is the next victim.
	time.Sleep(5 * time.Millisecond)
	if _, err := m.Workspace(ctx, "w3"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, w2open := m.handles["w2"]
	_, w1open := m.handles["w1"]
	_, w3open := m.handles["w3"]
	m.mu.Unlock()
	if w2open {
		t.Error("w2 should have been evicted (oldest after w3 refresh)")
	}
	if !w1open || !w3open {
		t.Errorf("w1 and w3 should be open (w1=%v w3=%v)", w1open, w3open)
	}
}

func TestManager_InterleavedSearch(t *testing.T) {
	root := t.TempDir()
	names := []string{"vault-a", "vault-b", "vault-c", "vault-d", "vault-e"}
	for _, n := range names {
		initWorkspaceIn(t, root, n)
	}

	m, err := OpenManager(context.Background(), root, WithMaxOpen(2))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	maxSeen := 0
	// Interleaved reads across all 5 with MaxOpen=2: every Stats call
	// (read-lock path) must succeed and the open count must never exceed 2.
	for round := 0; round < 3; round++ {
		for _, n := range names {
			w, err := m.Workspace(ctx, n)
			if err != nil {
				t.Fatalf("round %d Workspace(%s): %v", round, n, err)
			}
			if _, err := w.Stats(ctx); err != nil {
				t.Fatalf("round %d Stats(%s): %v", round, n, err)
			}
			if c := m.openCount(); c > maxSeen {
				maxSeen = c
			}
		}
	}
	if maxSeen > 2 {
		t.Errorf("max concurrent opens = %d, want ≤ 2", maxSeen)
	}
}

func TestManager_EvictionWaitsForReader(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "hot")
	initWorkspaceIn(t, root, "b1")
	initWorkspaceIn(t, root, "b2")

	m, err := OpenManager(context.Background(), root, WithMaxOpen(2))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	hot, err := m.Workspace(ctx, "hot")
	if err != nil {
		t.Fatal(err)
	}
	// Hold a read lock on hot directly — eviction must block until it drains.
	hot.mu.RLock()

	if _, err := m.Workspace(ctx, "b1"); err != nil {
		t.Fatal(err)
	}

	evictDone := make(chan error, 1)
	go func() {
		// Opening b2 with MaxOpen=2 evicts hot (oldest) — must block on the
		// held RLock.
		_, err := m.Workspace(ctx, "b2")
		evictDone <- err
	}()

	select {
	case <-evictDone:
		t.Fatal("eviction completed while a reader held the workspace lock")
	case <-time.After(100 * time.Millisecond):
		// blocked as required
	}
	hot.mu.RUnlock()
	select {
	case err := <-evictDone:
		if err != nil {
			t.Fatalf("eviction after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("eviction did not complete after the reader drained")
	}
}

func TestManager_IdleClose(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "one")

	m, err := OpenManager(context.Background(), root, WithIdleClose(60*time.Millisecond))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.Workspace(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	if got := m.openCount(); got != 1 {
		t.Fatalf("openCount = %d, want 1", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.openCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.openCount(); got != 0 {
		t.Errorf("openCount after idle window = %d, want 0", got)
	}
}

// TestManager_TouchKeepsIdleWorkspaceOpen (SPEC-06 review fix): the serve
// hot path opens a workspace once and serves many requests without
// re-calling Workspace — so recency must be refreshable independently.
// Touch refreshes lastUse; a workspace touched past its idle window stays
// open (without Touch, idle-close evicts a workspace under active traffic).
func TestManager_TouchKeepsIdleWorkspaceOpen(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "one")

	m, err := OpenManager(context.Background(), root, WithIdleClose(60*time.Millisecond))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.Workspace(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	// Touch repeatedly past the idle window — the workspace must survive.
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.Touch("one")
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.openCount(); got != 1 {
		t.Errorf("openCount after touched idle window = %d, want 1 (Touch must refresh recency)", got)
	}
}

func TestManager_MaxOpenZeroUnlimited(t *testing.T) {
	root := t.TempDir()
	names := []string{"u1", "u2", "u3", "u4"}
	for _, n := range names {
		initWorkspaceIn(t, root, n)
	}
	m, err := OpenManager(context.Background(), root) // no MaxOpen
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()
	for _, n := range names {
		if _, err := m.Workspace(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.openCount(); got != 4 {
		t.Errorf("openCount = %d, want 4 (unlimited)", got)
	}
}

func TestManager_PerWorkspaceOptions(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "opt-ws")

	var gotNames []string
	var mu sync.Mutex
	hook := func(name string) []Option {
		mu.Lock()
		gotNames = append(gotNames, name)
		mu.Unlock()
		return []Option{WithReadOnly()}
	}
	m, err := OpenManager(context.Background(), root, WithPerWorkspaceOptions(hook))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	w, err := m.Workspace(context.Background(), "opt-ws")
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	mu.Lock()
	if len(gotNames) != 1 || gotNames[0] != "opt-ws" {
		t.Errorf("hook received %v, want [opt-ws]", gotNames)
	}
	mu.Unlock()
	// WithReadOnly applied: a mutator must refuse.
	if _, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("x")}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Capture on read-only: err = %v, want ErrReadOnly", err)
	}
}
