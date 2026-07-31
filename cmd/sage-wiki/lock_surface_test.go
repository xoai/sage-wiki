package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/pkg/engine"
)

// lockWorkspaceForTest initializes a workspace and holds its writer lock.
func lockWorkspaceForTest(t *testing.T) (string, *engine.Workspace) {
	t.Helper()
	dir := t.TempDir()
	w, err := engine.Init(t.Context(), dir)
	if err != nil {
		t.Fatalf("engine.Init: %v", err)
	}
	// Keep w open — the lock is held.
	return dir, w
}

// TestCapture_WriterLockHeld: capture during a held writer lock fails with
// the spec §B.1 8e surface (exit-1-class error + the specified text).
func TestCapture_WriterLockHeld(t *testing.T) {
	dir, w := lockWorkspaceForTest(t)
	defer w.Close()

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	captureCmd.Flags().Set("text", "locked test content")
	defer captureCmd.Flags().Set("text", "")

	err := runCapture(captureCmd, nil)
	if err == nil {
		t.Fatal("capture must fail while a writer lock is held")
	}
	if !strings.Contains(err.Error(), "workspace is locked by another process (compile in progress?)") {
		t.Errorf("error text = %q", err.Error())
	}
}

// TestQuery_WriterLockHeld: query (which auto-files) fails the same way.
func TestQuery_WriterLockHeld(t *testing.T) {
	dir, w := lockWorkspaceForTest(t)
	defer w.Close()

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	err := runQuery(queryCmd, []string{"what", "is", "attention"})
	if err == nil {
		t.Fatal("query must fail while a writer lock is held")
	}
	if !strings.Contains(err.Error(), "workspace is locked by another process (compile in progress?)") {
		t.Errorf("error text = %q", err.Error())
	}
}

// TestCapture_LockReleasedAfterClose: after the holder closes, capture
// proceeds past the lock (LLM-free assertion: it must NOT fail with the
// lock text).
func TestCapture_LockReleasedAfterClose(t *testing.T) {
	dir, w := lockWorkspaceForTest(t)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	captureCmd.Flags().Set("text", "post-release content")
	defer captureCmd.Flags().Set("text", "")

	err := runCapture(captureCmd, nil)
	if err != nil && strings.Contains(err.Error(), "workspace is locked") {
		t.Errorf("capture must not fail with lock error after release: %v", err)
	}
}

// TestCompile_PreFormatRequiresUpgradeFlag is the F-045 regression test: a
// v0.2.x workspace (no format_version) fails compile with the --upgrade
// hint, and compiles once the flag is passed.
func TestCompile_PreFormatRequiresUpgradeFlag(t *testing.T) {
	dir := t.TempDir()
	w, err := engine.Init(t.Context(), dir)
	if err != nil {
		t.Fatalf("engine.Init: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Strip format fields → v0.2.x workspace.
	mPath := dir + "/.manifest.json"
	raw, _ := os.ReadFile(mPath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	delete(m, "format_version")
	delete(m, "engine_version")
	delete(m, "created_at")
	out, _ := json.Marshal(m)
	if err := os.WriteFile(mPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	compileCmd.Flags().Set("upgrade", "false")
	err = runCompile(compileCmd, nil)
	if err == nil {
		t.Fatal("compile must fail on a pre-format workspace without --upgrade")
	}
	if !strings.Contains(err.Error(), "--upgrade") {
		t.Errorf("error must direct at --upgrade, got: %v", err)
	}

	compileCmd.Flags().Set("upgrade", "true")
	defer compileCmd.Flags().Set("upgrade", "false")
	if err := runCompile(compileCmd, nil); err != nil {
		t.Errorf("compile with --upgrade must succeed: %v", err)
	}

	// Adoption persisted: format stamped.
	raw2, _ := os.ReadFile(mPath)
	if !strings.Contains(string(raw2), `"format_version"`) {
		t.Error("adoption must stamp format_version")
	}
}

// TestSearchFallsBackToLegacy pins the P1-8 degrade decision seam (review
// issue 5): only ErrConfigLoad with no explicit --config falls back.
func TestSearchFallsBackToLegacy(t *testing.T) {
	if !searchFallsBackToLegacy(engine.ErrConfigLoad, "") {
		t.Error("config-load failure without --config must fall back")
	}
	if searchFallsBackToLegacy(engine.ErrConfigLoad, "/tmp/explicit.yaml") {
		t.Error("explicit --config failure must NOT fall back (hard error)")
	}
	if searchFallsBackToLegacy(engine.ErrLocked, "") {
		t.Error("non-config errors must NOT fall back")
	}
	if searchFallsBackToLegacy(nil, "") {
		t.Error("nil error must not fall back")
	}
}
