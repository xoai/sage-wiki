package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic_WritesAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "article.md")

	if err := WriteFileAtomic(path, []byte("# hello\n"), 0644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "# hello\n" {
		t.Errorf("content = %q, want %q", got, "# hello\n")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0644 {
		t.Errorf("perm = %v, want 0644", info.Mode().Perm())
	}

	// No temp litter left behind after a successful write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_OverwriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "article.md")

	if err := WriteFileAtomic(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("v2-longer-content"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2-longer-content" {
		t.Errorf("content = %q, want v2-longer-content", got)
	}
}

// TestWriteFileAtomic_FailureLeavesOriginalIntact proves the all-or-nothing
// guarantee: when the atomic write cannot complete (here, the temp cannot be
// created because the directory is not writable), a pre-existing good file is
// left untouched — a partial write never replaces it.
func TestWriteFileAtomic_FailureLeavesOriginalIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "article.md")
	if err := os.WriteFile(path, []byte("good original"), 0644); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	// Make the directory non-writable so CreateTemp fails.
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	defer os.Chmod(dir, 0755)

	if err := WriteFileAtomic(path, []byte("should not land"), 0644); err == nil {
		t.Fatal("expected error writing into non-writable dir, got nil")
	}

	os.Chmod(dir, 0755)
	got, _ := os.ReadFile(path)
	if string(got) != "good original" {
		t.Errorf("original corrupted by failed write: got %q", got)
	}
}
