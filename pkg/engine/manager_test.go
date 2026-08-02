package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// initWorkspaceIn builds a real workspace at root/name via Init and closes it.
func initWorkspaceIn(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	w, err := Init(context.Background(), dir)
	if err != nil {
		t.Fatalf("Init(%s): %v", dir, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

func TestOpenManager_NotADir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := OpenManager(context.Background(), missing); err == nil {
		t.Error("OpenManager on missing dir must error")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := OpenManager(context.Background(), f.Name()); err == nil {
		t.Error("OpenManager on a file must error")
	}
}

func TestManager_List(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "alpha")
	initWorkspaceIn(t, root, "beta")
	// Invalid entries are skipped, not errors:
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	infos, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d entries, want 2: %+v", len(infos), infos)
	}
	got := map[string]WorkspaceInfo{}
	for _, i := range infos {
		got[i.Name] = i
		if i.Open {
			t.Errorf("%s reported open before any Workspace call", i.Name)
		}
		if i.RequiresUpgrade {
			t.Errorf("%s reported RequiresUpgrade on a fresh workspace", i.Name)
		}
	}
	if _, ok := got["alpha"]; !ok {
		t.Error("alpha missing from List")
	}
	if _, ok := got["beta"]; !ok {
		t.Error("beta missing from List")
	}

	// After opening one, List reports it open.
	if _, err := m.Workspace(context.Background(), "alpha"); err != nil {
		t.Fatalf("Workspace(alpha): %v", err)
	}
	infos, err = m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, i := range infos {
		if i.Name == "alpha" && !i.Open {
			t.Error("alpha must report Open=true after Workspace call")
		}
		if i.Name == "beta" && i.Open {
			t.Error("beta must report Open=false")
		}
	}
}

func TestManager_Workspace_LazyOpen(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "one")

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.Workspace(context.Background(), "ghost"); !errors.Is(err, ErrUnknownWorkspace) {
		t.Errorf("err = %v, want ErrUnknownWorkspace", err)
	}
	if _, err := m.Workspace(context.Background(), "../escape"); !errors.Is(err, ErrInvalidWorkspaceName) {
		t.Errorf("err = %v, want ErrInvalidWorkspaceName", err)
	}

	w, err := m.Workspace(context.Background(), "one")
	if err != nil {
		t.Fatalf("Workspace(one): %v", err)
	}
	// Cached: a second call returns the same handle.
	w2, err := m.Workspace(context.Background(), "one")
	if err != nil {
		t.Fatalf("second Workspace(one): %v", err)
	}
	if w != w2 {
		t.Error("cached handle must be returned on repeat Workspace calls")
	}
}

func TestManager_Workspace_PreFormatReadOnly(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, "testdata/v02x", filepath.Join(root, "legacy"))
	buildFixtureDB(t, filepath.Join(root, "legacy"))

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	w, err := m.Workspace(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("Workspace(legacy): %v", err)
	}
	if !w.RequiresUpgrade() {
		t.Error("pre-format workspace must open read-only (RequiresUpgrade)")
	}
}

func TestManager_Close_Idempotent(t *testing.T) {
	root := t.TempDir()
	initWorkspaceIn(t, root, "one")

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	if _, err := m.Workspace(context.Background(), "one"); err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
	if _, err := m.Workspace(context.Background(), "one"); !errors.Is(err, ErrManagerClosed) {
		t.Errorf("use after Close: err = %v, want ErrManagerClosed", err)
	}
	if _, err := m.List(context.Background()); !errors.Is(err, ErrManagerClosed) {
		t.Errorf("List after Close: err = %v, want ErrManagerClosed", err)
	}
	// The workspace lock must be released: a direct Open succeeds.
	w, err := Open(context.Background(), filepath.Join(root, "one"))
	if err != nil {
		t.Fatalf("Open after Manager.Close (lock leaked?): %v", err)
	}
	_ = w.Close()
}

func TestManager_SymlinkAlias(t *testing.T) {
	root := t.TempDir()
	target := initWorkspaceIn(t, root, "real-ws")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Escape symlink: must never be served.
	outside := initWorkspace(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.Workspace(context.Background(), "escape"); err == nil {
		t.Error("symlink escaping root must be rejected")
	}

	// The alias resolves to the same workspace: opening it while real-ws is
	// open must collide on the per-workspace lock (no two handles on one
	// workspace).
	if _, err := m.Workspace(context.Background(), "real-ws"); err != nil {
		t.Fatalf("Workspace(real-ws): %v", err)
	}
	if _, err := m.Workspace(context.Background(), "alias"); !errors.Is(err, ErrLocked) {
		t.Errorf("alias while real-ws open: err = %v, want ErrLocked", err)
	}
}

func TestManager_WorkspaceDeletedWhileOpen(t *testing.T) {
	root := t.TempDir()
	dir := initWorkspaceIn(t, root, "gone")

	m, err := OpenManager(context.Background(), root)
	if err != nil {
		t.Fatalf("OpenManager: %v", err)
	}
	defer func() { _ = m.Close() }()

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// List re-scans: the deleted workspace disappears.
	infos, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("List after delete returned %d entries, want 0", len(infos))
	}
	// Opening it now is unknown, not a stale success.
	if _, err := m.Workspace(context.Background(), "gone"); !errors.Is(err, ErrUnknownWorkspace) {
		t.Errorf("err = %v, want ErrUnknownWorkspace", err)
	}
}
