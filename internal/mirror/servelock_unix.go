//go:build unix

package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// probeServeLock reports whether another process holds the engine lock
// (i.e. a serve is running) — used by status's serve-restart note.
func probeServeLock(dir string) bool {
	path := filepath.Join(dir, ".sage", "engine.lock")
	// F-089 (probe): read-only probe — a missing file means "not held", and status
	// must not create it (read-only commands don't mutate the workspace).
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true // held by someone else
	}
	if err == nil {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
	}
	return false
}
