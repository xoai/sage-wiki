//go:build !unix

package mirror

import "os"

// Non-unix platforms use the portable lockfile fallback for the ship-mutex.

func acquireShipMutexOnce(dir string) (*ShipMutex, error) {
	return acquireShipLockfile(dir)
}

func releaseShipPlatformLock(f *os.File) error {
	return f.Close()
}
