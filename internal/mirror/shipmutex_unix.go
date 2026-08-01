//go:build unix

package mirror

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// acquireShipMutexOnce takes a non-blocking flock on the ship-mutex file.
func acquireShipMutexOnce(dir string) (*ShipMutex, error) {
	path := shipMutexPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mirror: create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("mirror: open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrShipLocked
		}
		return nil, fmt.Errorf("mirror: flock %s: %w", path, err)
	}
	return &ShipMutex{path: path, held: f}, nil
}

// releaseShipPlatformLock unlocks and closes the file; the lock file itself
// stays on disk (an flock does not care about the file's existence).
func releaseShipPlatformLock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		f.Close()
		return fmt.Errorf("mirror: unflock: %w", err)
	}
	return f.Close()
}
