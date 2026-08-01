package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

// F-035 witness: N concurrent first-time Workspace(name) calls must
// converge on ONE open — every caller gets a working handle, none sees
// ErrLocked.
func TestManager_ConcurrentFirstOpenConverges(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "hot")
	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	handles := make([]interface{ Dir() string }, n)
	barrier := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			w, err := m.Workspace(context.Background(), "hot")
			errs[i] = err
			if err == nil {
				handles[i] = w
			}
		}(i)
	}
	close(barrier)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v (concurrent first-open must converge)", i, err)
		}
	}
	// Exactly ONE underlying open happened.
	if got := m.openCount(); got != 1 {
		t.Errorf("openCount = %d, want 1", got)
	}
	_ = handles
}

// F-043 witness: eviction closes the victim OUTSIDE the Manager mutex —
// a slow victim close (or a busy hook) must not stall unrelated Manager
// operations.
func TestManager_EvictionDoesNotHoldMutex(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "victim")
	initWorkspaceIn(t, root, "other")
	initWorkspaceIn(t, root, "newcomer")

	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	hook := func(name string, ws *Workspace) error {
		if name == "victim" {
			close(closeStarted)
			<-releaseClose // a slow eviction close
		}
		return ws.Close()
	}
	m, err := OpenManager(context.Background(), root, WithMaxOpen(2), WithOnEvict(hook))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	if _, err := m.Workspace(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Workspace(ctx, "other"); err != nil {
		t.Fatal(err)
	}

	// Trigger eviction of "victim" by opening "newcomer" (MaxOpen=2).
	evictDone := make(chan error, 1)
	go func() {
		_, err := m.Workspace(ctx, "newcomer")
		evictDone <- err
	}()
	<-closeStarted

	// While the victim close is blocked, an UNRELATED Manager call must
	// proceed (no server-wide stall).
	listDone := make(chan error, 1)
	go func() {
		_, err := m.List(ctx)
		listDone <- err
	}()
	select {
	case err := <-listDone:
		if err != nil {
			t.Errorf("List during blocked eviction: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("List stalled behind a blocked victim close — the Manager mutex is held across eviction")
	}
	close(releaseClose)
	if err := <-evictDone; err != nil {
		t.Fatalf("evicting open: %v", err)
	}
}

// F-044 witness: LRU ties (same-tick opens) break deterministically by
// name, not map iteration order.
func TestManager_LRUTieBreakByName(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "aaa")
	initWorkspaceIn(t, root, "bbb")
	initWorkspaceIn(t, root, "ccc")
	m, err := OpenManager(context.Background(), root, WithMaxOpen(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	// Two opens with forced identical lastUse.
	if _, err := m.Workspace(ctx, "aaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Workspace(ctx, "bbb"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	forced := m.handles["aaa"].lastUse
	m.handles["bbb"].lastUse = forced
	m.mu.Unlock()

	if _, err := m.Workspace(ctx, "ccc"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, aaaOpen := m.handles["aaa"]
	_, bbbOpen := m.handles["bbb"]
	m.mu.Unlock()
	if aaaOpen && bbbOpen {
		t.Error("one of aaa/bbb must be evicted at MaxOpen=2")
	}
	if !aaaOpen && !bbbOpen {
		t.Error("exactly one of aaa/bbb must survive")
	}
	// Deterministic victim: the lexicographically smaller name loses the
	// tie (documented tie-break).
	if aaaOpen {
		t.Error("aaa must be the deterministic tie victim (evicted)")
	}
}
