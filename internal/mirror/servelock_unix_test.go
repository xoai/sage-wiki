//go:build unix

package mirror

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// holdEngineLock takes an flock on the workspace engine.lock (simulating a
// running serve) and returns a release func.
func holdEngineLock(t *testing.T, dir string) (*os.File, func()) {
	t.Helper()
	path := filepath.Join(dir, ".sage", "engine.lock")
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("holdEngineLock: %v", err)
	}
	return f, func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}
}
