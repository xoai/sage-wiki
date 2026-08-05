//go:build !unix

package mirror

import (
	"os"
	"testing"
)

// holdEngineLock is the non-Unix stub: flock is unavailable, so the
// lock-detection test is skipped on Windows (the engine uses a lockfile
// fallback there, tested separately).
func holdEngineLock(t *testing.T, dir string) (*os.File, func()) {
	t.Skip("holdEngineLock: flock is unix-only")
	return nil, func() {}
}
