package pathsafe

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// mkBase returns an existing base directory (EvalSymlinks requires base to
// exist) plus its parent root, both symlink-resolved so comparisons are stable
// on platforms whose temp dir is itself a symlink (e.g. macOS /var → /private).
func mkBase(t *testing.T) (root, base string) {
	t.Helper()
	root = t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	base = filepath.Join(root, "wiki")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	return root, base
}

func TestContained(t *testing.T) {
	root, base := mkBase(t)
	// A sibling directory that shares a name prefix with base.
	if err := os.MkdirAll(filepath.Join(root, "wiki-secret"), 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	// A real nested dir under base.
	if err := os.MkdirAll(filepath.Join(base, "concepts"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"exact base", base, true},
		{"legit nested existing dir", filepath.Join(base, "concepts"), true},
		{"legit nested non-existent file", filepath.Join(base, "concepts", "a.md"), true},
		{"non-existent nested chain (write target)", filepath.Join(base, "new", "deep", "f.md"), true},
		{"parent traversal", filepath.Join(base, "..", "etc", "passwd"), false},
		{"nested traversal escaping", filepath.Join(base, "a", "..", "..", "b"), false},
		{"sibling shared-prefix dir", filepath.Join(root, "wiki-secret", "f"), false},
		{"outright outside", filepath.Join(root, "elsewhere", "x"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Contained(base, tc.target)
			if err != nil && tc.want {
				t.Fatalf("Contained(%q) unexpected error: %v", tc.target, err)
			}
			if got != tc.want {
				t.Errorf("Contained(base=%q, target=%q) = %v, want %v", base, tc.target, got, tc.want)
			}
		})
	}
}

// TestContainedMissingBaseLeaf covers a base whose leaf does not exist yet (an
// output dir before the first compile): legit nested paths must stay contained
// (so the caller returns 404, not a misleading 403), while traversal is still
// blocked.
func TestContainedMissingBaseLeaf(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	base := filepath.Join(root, "output") // deliberately NOT created

	if got, err := Contained(base, filepath.Join(base, "concepts", "a.md")); err != nil || !got {
		t.Errorf("Contained(missing-base, nested) = %v (err %v), want true", got, err)
	}
	if got, _ := Contained(base, filepath.Join(base, "..", "etc", "passwd")); got {
		t.Error("Contained(missing-base, traversal) = true, want false")
	}
}

func TestSafeJoin(t *testing.T) {
	_, base := mkBase(t)
	if err := os.MkdirAll(filepath.Join(base, "concepts"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	// Legit relatives resolve to base/<rel>.
	okCases := []struct {
		name string
		rel  string
		want string
	}{
		{"nested existing", "concepts/a.md", filepath.Join(base, "concepts", "a.md")},
		{"nested non-existent chain", "new/deep/f.md", filepath.Join(base, "new", "deep", "f.md")},
		{"dot-slash prefix", "./concepts/a.md", filepath.Join(base, "concepts", "a.md")},
	}
	for _, tc := range okCases {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			got, err := SafeJoin(base, tc.rel)
			if err != nil {
				t.Fatalf("SafeJoin(%q) error: %v", tc.rel, err)
			}
			if got != tc.want {
				t.Errorf("SafeJoin(%q) = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}

	// Escapes must error and return "".
	badCases := []struct {
		name string
		rel  string
	}{
		{"parent traversal", "../etc/passwd"},
		{"deep traversal", "a/../../b"},
		{"absolute-ish escape", "../../../../etc/passwd"},
	}
	for _, tc := range badCases {
		t.Run("bad/"+tc.name, func(t *testing.T) {
			got, err := SafeJoin(base, tc.rel)
			if err == nil {
				t.Errorf("SafeJoin(%q) = %q, want error", tc.rel, got)
			}
			if got != "" {
				t.Errorf("SafeJoin(%q) returned %q on error, want empty", tc.rel, got)
			}
		})
	}
}

// TestSymlinkEscape proves a symlink inside base pointing outside base cannot be
// used to escape — the exact case bare strings.HasPrefix misses.
func TestSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges on Windows")
	}
	root, base := mkBase(t)
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	// base/escape -> outside
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// base/inside -> base/concepts (stays within base)
	if err := os.MkdirAll(filepath.Join(base, "concepts"), 0o755); err != nil {
		t.Fatalf("mkdir concepts: %v", err)
	}
	if err := os.Symlink(filepath.Join(base, "concepts"), filepath.Join(base, "inside")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Through the escaping symlink → blocked.
	if got, _ := Contained(base, filepath.Join(base, "escape", "secret.txt")); got {
		t.Error("Contained followed a symlink escaping base — should be blocked")
	}
	if _, err := SafeJoin(base, "escape/secret.txt"); err == nil {
		t.Error("SafeJoin followed a symlink escaping base — should error")
	}

	// Through the inside symlink → allowed.
	if got, err := Contained(base, filepath.Join(base, "inside", "a.md")); err != nil || !got {
		t.Errorf("Contained via in-base symlink = %v (err %v), want true", got, err)
	}
}
