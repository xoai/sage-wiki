//go:build !unix

package engine

import "os"

// acquirePlatformLock is the portable lockfile fallback for platforms
// without flock (windows, plan9).
func acquirePlatformLock(dir string) (*workspaceLock, error) {
	return acquireLockfile(dir)
}

// releasePlatformLock is only reached for non-fallback locks, which do not
// exist on !unix platforms — the fallback releases via releaseLockfile
// (which removes the lockfile). Provided for interface completeness.
func releasePlatformLock(f *os.File) error {
	return f.Close()
}
