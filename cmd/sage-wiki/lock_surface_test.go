package main

import (
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
