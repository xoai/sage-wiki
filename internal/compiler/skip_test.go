package compiler

import (
	"errors"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/traversaltest"
)

// SPEC-08 AC1: ExplainCompile accepts workspace-relative docs only — the
// guard rejects the shared traversal table before any filesystem access
// (the old raw filepath.Join read/hashed outside the workspace).
func TestExplainCompileKeyRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range traversaltest.Cases() {
		_, err := ExplainCompileKey(dir, tc.Input, nil, nil, nil)
		if err == nil {
			t.Errorf("%s: ExplainCompileKey(%q) = nil error, want typed rejection", tc.Name, tc.Input)
			continue
		}
		switch tc.Family {
		case "traversal":
			if !errors.Is(err, limits.ErrTraversalTooWide) {
				t.Errorf("%s: err = %v, want ErrTraversalTooWide", tc.Name, err)
			}
		case "malformed":
			if !errors.Is(err, limits.ErrInvalidName) {
				t.Errorf("%s: err = %v, want ErrInvalidName", tc.Name, err)
			}
		}
	}
}

func TestExplainCompileKeyBenignPathPassesGuard(t *testing.T) {
	dir := t.TempDir()
	// Benign relative path: the guard passes; the call then fails at file
	// hashing (no such file) — which must NOT be a limits sentinel.
	_, err := ExplainCompileKey(dir, "raw/missing.md", nil, nil, nil)
	if err == nil {
		t.Fatal("expected a file error for a missing doc")
	}
	if errors.Is(err, limits.ErrTraversalTooWide) || errors.Is(err, limits.ErrInvalidName) {
		t.Errorf("benign path hit the traversal guard: %v", err)
	}
}
