//go:build unix

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// acquirePlatformLock takes an exclusive, non-blocking flock on
// <dir>/.sage/engine.lock. A held lock fails fast with ErrLocked.
func acquirePlatformLock(dir string) (*workspaceLock, error) {
	path := lockPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("engine: create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("engine: open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("engine: flock %s: %w", path, err)
	}
	return &workspaceLock{path: path, held: f}, nil
}

// releasePlatformLock unlocks and closes the file. The lock file itself is
// left on disk (an flock does not care about the file's existence, and
// removing it would race a concurrent opener).
func releasePlatformLock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		f.Close()
		return fmt.Errorf("engine: unflock: %w", err)
	}
	return f.Close()
}
