package serve

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// stackFixture builds a root with n greenfield workspaces and a registry
// whose Manager evicts through the registry hook (MaxOpen=2).
func stackFixture(t *testing.T, names ...string) (*stackRegistry, *engine.Manager, string) {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	for _, n := range names {
		w, err := engine.Init(ctx, filepath.Join(root, n))
		if err != nil {
			t.Fatalf("init %s: %v", n, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reg := newStackRegistry(ctx, 2)
	mgr, err := engine.OpenManager(ctx, root,
		engine.WithMaxOpen(2),
		engine.WithOnEvict(reg.evict))
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	reg.mgr = mgr
	t.Cleanup(func() {
		_ = reg.closeAll()
		_ = mgr.Close()
	})
	return reg, mgr, root
}

func TestStack_AcquireRelease(t *testing.T) {
	reg, _, _ := stackFixture(t, "ws-a", "ws-b")
	ctx := context.Background()

	st, err := reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// The handler is the full serve surface.
	w := httptest.NewRecorder()
	st.handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("stack healthz = %d, want 200", w.Code)
	}
	st.release()

	// Unknown workspace errors through the Manager.
	if _, err := reg.acquire(ctx, "ghost"); err == nil {
		t.Error("acquire(ghost) must error")
	}
}

func TestStack_ManagerEvictionWaitsForRefcount(t *testing.T) {
	reg, _, _ := stackFixture(t, "ws-a", "ws-b", "ws-c")
	ctx := context.Background()

	stA, err := reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	stB, err := reg.acquire(ctx, "ws-b")
	if err != nil {
		t.Fatal(err)
	}
	defer stB.release()

	// Acquiring ws-c with MaxOpen=2 evicts ws-a — the hook must BLOCK
	// while stA's ref is held.
	done := make(chan error, 1)
	go func() {
		stC, err := reg.acquire(ctx, "ws-c")
		if err == nil {
			stC.release()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("eviction completed while a request held the stack ref")
	case <-time.After(150 * time.Millisecond):
	}
	stA.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquire after drain: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("eviction did not complete after refcount drained")
	}
}

func TestStack_EvictClosesAll(t *testing.T) {
	reg, mgr, _ := stackFixture(t, "ws-a", "ws-b", "ws-c")
	ctx := context.Background()

	stA, err := reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	wsA := stA.ws
	stA.release()
	stB, err := reg.acquire(ctx, "ws-b")
	if err != nil {
		t.Fatal(err)
	}
	// Evict ws-a by opening ws-c (MaxOpen=2).
	stC, err := reg.acquire(ctx, "ws-c")
	if err != nil {
		t.Fatal(err)
	}
	stC.release()
	// Release BEFORE re-acquiring ws-a: that eviction's LRU victim is ws-b,
	// and the hook waits out held refs — holding stB here would
	// self-deadlock the test (and any caller that holds a stack across an
	// acquire, by design).
	stB.release()

	// The evicted workspace handle is closed (lock released).
	if _, err := wsA.Stats(ctx); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("evicted ws Stats err = %v, want closed", err)
	}
	// Re-acquiring builds a FRESH working stack.
	stA2, err := reg.acquire(ctx, "ws-a")
	if err != nil {
		t.Fatalf("re-acquire after eviction: %v", err)
	}
	defer stA2.release()
	w := httptest.NewRecorder()
	stA2.handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("re-acquired stack healthz = %d, want 200", w.Code)
	}
	if mgr == nil {
		t.Fatal("mgr wiring")
	}
}

func TestStack_PreFormatWorkspace(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	// Pre-format fixture: config + stripped manifest, like engine's v02x.
	fixtureSrc := filepath.Join("..", "..", "pkg", "engine", "testdata", "v02x")
	legacyDir := filepath.Join(root, "legacy")
	copyDir(t, fixtureSrc, legacyDir)
	// The fixture's .sage dir is gitignored — build the db hermetically at
	// the current schema (the pre-format discriminator is the manifest).
	if db, err := storage.Open(filepath.Join(legacyDir, ".sage", "wiki.db")); err != nil {
		t.Fatalf("build fixture db: %v", err)
	} else {
		db.Close()
	}

	reg := newStackRegistry(ctx, 2)
	mgr, err := engine.OpenManager(ctx, root, engine.WithOnEvict(reg.evict))
	if err != nil {
		t.Fatal(err)
	}
	reg.mgr = mgr
	t.Cleanup(func() { _ = reg.closeAll(); _ = mgr.Close() })

	_, err = reg.acquire(ctx, "legacy")
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("pre-format stack err = %v, want upgrade hint", err)
	}
}

// copyDir recursively copies a directory (fixture helper).
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		target := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDir(t, filepath.Join(src, e.Name()), target)
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// F-034 witness: two concurrent first-time acquire(name) calls converge —
// the loser's teardown must NOT close the Manager-shared workspace handle
// out from under the winner.
func TestStack_ConcurrentAcquireDoesNotCloseSharedWorkspace(t *testing.T) {
	reg, _, _ := stackFixture(t, "ws-a")
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	stacks := make([]*workspaceStack, n)
	errs := make([]error, n)
	barrier := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			st, err := reg.acquire(ctx, "ws-a")
			stacks[i] = st
			errs[i] = err
		}(i)
	}
	close(barrier)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	// Release all but one ref, then serve on the survivor: the workspace
	// must still be open (the loser's teardown must not have closed it).
	for i := 1; i < n; i++ {
		stacks[i].release()
	}
	w := httptest.NewRecorder()
	stacks[0].handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("survivor healthz = %d, want 200 (shared workspace was closed by a loser teardown)", w.Code)
	}
	if _, err := stacks[0].ws.Stats(ctx); err != nil {
		t.Errorf("shared workspace closed by loser teardown: %v", err)
	}
	stacks[0].release()
}
